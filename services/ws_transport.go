package services

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/keymetrics/pm2-io-apm-go/features/metrics"
	"github.com/keymetrics/pm2-io-apm-go/structures"
)

// WSTransporter handle, send and receive packet from KM
type WSTransporter struct {
	Config  *structures.Config
	Version string

	ws              *websocket.Conn
	mu              sync.Mutex
	isConnected     bool
	isHandling      bool
	isConnecting    bool
	isClosed        bool
	wsNode          *string
	heartbeatTicker *time.Ticker // 5 seconds
	serverTicker    *time.Ticker // 10 minutes
}

// WSMessage receive from KM
type WSMessage struct {
	Payload interface{} `json:"payload"`
	Channel string      `json:"channel"`
}

// NewWSTransporter with default values
func NewWSTransporter(config *structures.Config, version string) *WSTransporter {
	return &WSTransporter{
		Config:  config,
		Version: version,

		isHandling:   false,
		isConnecting: false,
		isClosed:     false,
		isConnected:  false,
	}
}

// GetConfig get transport config
func (transporter *WSTransporter) GetConfig() *structures.Config {
	return transporter.Config
}

// GetServer check api.cloud.pm2.io to get current node
func (transporter *WSTransporter) GetServer() *string {
	verify := Verify{
		PublicId:  transporter.Config.PublicKey,
		PrivateId: transporter.Config.PrivateKey,
		Data: VerifyData{
			MachineName: transporter.Config.ServerName,
			Cpus:        runtime.NumCPU(),
			Memory:      metrics.TotalMem(),
			Pm2Version:  transporter.Version,
			Hostname:    transporter.Config.Hostname,
		},
	}
	jsonValue, _ := json.Marshal(verify)

	res, err := transporter.httpClient().Post("https://"+*transporter.Config.Node+"/api/node/verifyPM2", "application/json", bytes.NewBuffer(jsonValue))
	if err != nil {
		log.Println("Error while trying to get endpoints (verifypm2)", err)
		return nil
	}
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil
	}
	res.Body.Close()
	response := VerifyResponse{}
	err = json.Unmarshal(data, &response)
	if err != nil {
		log.Println("PM2 server sent an incorrect response")
		return nil
	}
	return &response.Endpoints.WS
}

// Connect to current wsNode with headers and prepare handlers/tickers
func (transporter *WSTransporter) Connect() {
	if transporter.wsNode == nil {
		transporter.wsNode = transporter.GetServer()
	}
	if transporter.wsNode == nil {
		go func() {
			log.Println("Cannot get node, retry in 10sec")
			time.Sleep(10 * time.Second)
			transporter.Connect()
		}()
		return
	}

	headers := http.Header{}
	headers.Add("X-KM-PUBLIC", transporter.Config.PublicKey)
	headers.Add("X-KM-SECRET", transporter.Config.PrivateKey)
	headers.Add("X-KM-SERVER", transporter.Config.ServerName)
	headers.Add("X-PM2-VERSION", transporter.Version)
	headers.Add("X-PROTOCOL-VERSION", "1")
	headers.Add("User-Agent", "PM2 Agent Golang v"+transporter.Version)

	c, _, err := transporter.websocketDialer().Dial(*transporter.wsNode, headers)
	if err != nil {
		log.Println("Error while connecting to WS", err)
		time.Sleep(2 * time.Second)
		transporter.isConnecting = false
		transporter.CloseAndReconnect()
		return
	}
	c.SetCloseHandler(func(code int, text string) error {
		transporter.isClosed = true
		return nil
	})

	transporter.isConnected = true
	transporter.isConnecting = false

	transporter.ws = c

	if !transporter.isHandling {
		transporter.SetHandlers()
	}

	go func() {
		if transporter.serverTicker != nil {
			return
		}
		transporter.serverTicker = time.NewTicker(10 * time.Minute)
		for {
			<-transporter.serverTicker.C
			srv := transporter.GetServer()
			if srv != nil && *srv != *transporter.wsNode {
				transporter.wsNode = srv
				transporter.CloseAndReconnect()
			}
		}
	}()
}

// SetHandlers for messages and pinger ticker
func (transporter *WSTransporter) SetHandlers() {
	transporter.isHandling = true

	go transporter.MessagesHandler()

	go func() {
		if transporter.heartbeatTicker != nil {
			return
		}
		transporter.heartbeatTicker = time.NewTicker(5 * time.Second)
		for {
			<-transporter.heartbeatTicker.C
			transporter.mu.Lock()
			err := transporter.ws.WriteMessage(websocket.PingMessage, []byte{})
			transporter.mu.Unlock()
			if err != nil {
				transporter.CloseAndReconnect()
			}
		}
	}()
}

func (transporter *WSTransporter) websocketDialer() (dialer *websocket.Dialer) {
	dialer = websocket.DefaultDialer
	if transporter.Config.Proxy == "" {
		return
	}
	url, err := url.Parse(transporter.Config.Proxy)
	if err != nil {
		log.Println("Proxy config incorrect, using default network for websocket", err)
		return
	}
	dialer.Proxy = http.ProxyURL(url)
	return
}

func (transporter *WSTransporter) httpClient() *http.Client {
	if transporter.Config.Proxy == "" {
		return &http.Client{}
	}
	url, err := url.Parse(transporter.Config.Proxy)
	if err != nil {
		log.Println("Proxy config incorrect, using default network for http", err)
		return &http.Client{}
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(url)}}
}

// MessagesHandler from KM
func (transporter *WSTransporter) MessagesHandler() {
	for {
		_, message, err := transporter.ws.ReadMessage()
		if err != nil {
			transporter.isHandling = false
			transporter.CloseAndReconnect()
			return
		}

		var dat map[string]interface{}

		if err := json.Unmarshal(message, &dat); err != nil {
			panic(err)
		}

		if dat["channel"] == "trigger:action" {
			payload := dat["payload"].(map[string]interface{})
			name := payload["action_name"]

			response := CallAction(name.(string), payload)

			transporter.Send("trigger:action:success", map[string]interface{}{
				"success":     true,
				"id":          payload["process_id"],
				"action_name": name,
			})
			transporter.Send("axm:reply", map[string]interface{}{
				"action_name": name,
				"return":      response,
			})

		} else if dat["channel"] == "trigger:pm2:action" {
			payload := dat["payload"].(map[string]interface{})
			name := payload["method_name"]
			switch name {
			case "startLogging":
				transporter.SendJSON(map[string]interface{}{
					"channel": "trigger:pm2:result",
					"payload": map[string]interface{}{
						"ret": map[string]interface{}{
							"err": nil,
						},
					},
				})
				break
			}
		} else {
			log.Println("msg not registered: " + dat["channel"].(string))
		}
	}
}

// SendJSON marshal it and check errors
func (transporter *WSTransporter) SendJSON(msg interface{}) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}

	transporter.mu.Lock()
	defer transporter.mu.Unlock()

	if !transporter.isConnected {
		return
	}
	transporter.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	err = transporter.ws.WriteMessage(websocket.TextMessage, b)
	if err != nil {
		transporter.CloseAndReconnect()
	}
}

// Send to specified channel
func (transporter *WSTransporter) Send(channel string, data interface{}) {
	transporter.SendJSON(WSMessage{
		Channel: channel,
		Payload: PayLoad{
			At: time.Now().UnixNano() / int64(time.Millisecond),
			Process: structures.Process{
				PmID:   0,
				Name:   transporter.Config.Name,
				Server: transporter.Config.ServerName, // WARN: maybe error here
			},
			Data:       data,
			Active:     true,
			ServerName: transporter.Config.ServerName,
			Protected:  false,
			RevCon:     true,
			InternalIP: metrics.LocalIP(),
		},
	})
}

// CloseAndReconnect webSocket
func (transporter *WSTransporter) CloseAndReconnect() {
	if transporter.isConnecting {
		return
	}

	transporter.isConnecting = true
	if !transporter.isClosed && transporter.ws != nil {
		transporter.isConnected = false
		transporter.ws.Close()
	}
	transporter.Connect()
}

// IsConnected expose if everything is running normally
func (transporter *WSTransporter) IsConnected() bool {
	return transporter.isConnected && transporter.isHandling && !transporter.isConnecting
}

// GetWsNode export current node
func (transporter *WSTransporter) GetWsNode() *string {
	return transporter.wsNode
}
