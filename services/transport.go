package services

import "github.com/keymetrics/pm2-io-apm-go/structures"

// Transporter base transporter interface
type Transporter interface {
	GetConfig() *structures.Config
	GetServer() *string
	Connect()
	SetHandlers()
	MessagesHandler()
	SendJSON(msg interface{})
	Send(channel string, data interface{})
	CloseAndReconnect()
	IsConnected() bool
}
