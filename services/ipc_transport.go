package services

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/keymetrics/pm2-io-apm-go/structures"
)

// IPCMessage receive from KM
type IPCMessage struct {
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

// Connect start handlers
func (transporter *IPCTransporter) Connect() {
	if !transporter.isHandling {
		transporter.SetHandlers()
	}
}

// SetHandlers for messages
func (transporter *IPCTransporter) SetHandlers() {
	transporter.isHandling = true
	go transporter.MessagesHandler()
}

// MessagesHandler pm2 trigger
func (transporter *IPCTransporter) MessagesHandler() {
	for {
		message, err := transporter.channel.reader.ReadBytes('\n')
		if err != nil {
			transporter.isHandling = false
			transporter.CloseAndReconnect()
			return
		}

		var dat map[string]interface{}
		var name string
		var id string

		if err := json.Unmarshal(message, &dat); err != nil {
			id = ""
			if err := json.Unmarshal(message, &name); err != nil {
				continue
			}
		} else {
			name = dat["msg"].(string)
			id = dat["id"].(string)
		}

		response := CallAction(name, dat)

		transporter.Send("trigger:action:success", map[string]interface{}{
			"success":     true,
			"id":          id,
			"action_name": name,
		})
		transporter.Send("axm:reply", map[string]interface{}{
			"action_name": name,
			"return":      response,
		})
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
			transporter.SendJSON(IPCMessage{
				Channel: "axm:monitor",
				Payload: status.Process[0].AxmMonitor,
			})
			for _, action := range status.Process[0].AxmActions {
				transporter.SendJSON(IPCMessage{
					Channel: "axm:action",
					Payload: action,
				})
			}
		}
	default:
		transporter.SendJSON(IPCMessage{
			Channel: channel,
			Payload: data,
		})
	}
}

// CloseAndReconnect reconnect
func (transporter *IPCTransporter) CloseAndReconnect() {
	transporter.Connect()
}

// IsConnected expose if everything is running normally
func (transporter *IPCTransporter) IsConnected() bool {
	return true
}
