package services

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/keymetrics/pm2-io-apm-go/structures"
)

// IPCSMessage receive from KM
type IPCSMessage struct {
	Payload interface{} `json:"data"`
	Channel string      `json:"type"`
}

type ipcChannel struct {
	fd     int
	reader *bufio.Reader
}

func newIPCChannel(fd int) ipcChannel {
	channel := ipcChannel{
		fd,
		nil,
	}
	channel.reader = bufio.NewReader(&channel)
	return channel
}

func (c *ipcChannel) Read(buff []byte) (int, error) {
	n, _, _, _, err := syscall.Recvmsg(c.fd, buff, nil, 0)
	return n, err
}

// IPCTransporter handle, send and receive packet from KM
type IPCTransporter struct {
	Config  *structures.Config
	Version string

	channel      ipcChannel
	mu           sync.Mutex
	isConnected  bool
	isHandling   bool
	isConnecting bool
	isClosed     bool
}

// NewIPCTransporter with default values
func NewIPCTransporter(config *structures.Config, version string) (*IPCTransporter, error) {
	fdStr, ok := os.LookupEnv("NODE_CHANNEL_FD")
	if !ok {
		return nil, errors.New("Cannot find node IPC channel")
	}
	fd, err := strconv.Atoi(fdStr)
	if err != nil {
		return nil, err
	}
	return &IPCTransporter{
		Config:  config,
		Version: version,
		channel: newIPCChannel(fd),
	}, nil
}

// GetConfig get transport config
func (transporter *IPCTransporter) GetConfig() *structures.Config {
	return transporter.Config
}

// GetServer noop
func (transporter *IPCTransporter) GetServer() *string {
	return nil
}

// Connect noop
func (transporter *IPCTransporter) Connect() {
}

// SetHandlers for messages and pinger ticker
func (transporter *IPCTransporter) SetHandlers() {
	go transporter.MessagesHandler()
}

// MessagesHandler from KM
func (transporter *IPCTransporter) MessagesHandler() {
	for {
		message, err := transporter.channel.reader.ReadBytes('\n')
		if err != nil {
			panic(err)
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
func (transporter *IPCTransporter) SendJSON(msg interface{}) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}

	transporter.mu.Lock()
	defer transporter.mu.Unlock()

	_ = syscall.Sendmsg(transporter.channel.fd, append(b, '\n'), nil, nil, 0)
}

// Send to specified channel
func (transporter *IPCTransporter) Send(channel string, data interface{}) {
	switch channel {
	case "status":
		status, ok := data.(structures.Status)
		if ok {
			transporter.SendJSON(IPCSMessage{
				Channel: "axm:monitor",
				Payload: status.Process[0].AxmMonitor,
			})
			transporter.SendJSON(IPCSMessage{
				Channel: "axm:actions",
				Payload: status.Process[0].AxmActions,
			})
			transporter.SendJSON(IPCSMessage{
				Channel: "axm:options",
				Payload: status.Process[0].AxmOptions,
			})
		}
	default:
		transporter.SendJSON(IPCSMessage{
			Channel: channel,
			Payload: data,
		})
	}
}

// CloseAndReconnect noop
func (transporter *IPCTransporter) CloseAndReconnect() {
}

// IsConnected expose if everything is running normally
func (transporter *IPCTransporter) IsConnected() bool {
	return true
}
