package main

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"lcf-controller/inject"
	"lcf-controller/logger"
	"lcf-controller/net/server"
	"lcf-controller/pkg/config"
	"lcf-controller/pkg/info"
	_type "lcf-controller/pkg/type/frps"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

// NewWebSocket 初始化WebSocket客户端
func NewWebSocket() *WsClient {
	ws := new(WsClient)
	cfg := config.ReadCfg()
	ws.addr = cfg.ControllerConfig.Addr
	return ws
}

// ConnectWsServer 连接到WebSocket服务器
func (w *WsClient) ConnectWsServer() (err error) {
	conn, _, err := websocket.DefaultDialer.Dial(w.addr, nil)
	if err != nil {
		return err
	}
	w.conn = conn
	return nil
}

// SendMsg 发送消息到服务器
func (w *WsClient) SendMsg(cfg *config.Config, action string, data map[string]any) (err error) {
	req := new(BasicRequest)
	req.Action = action
	req.Node.Id = cfg.ControllerConfig.NodeId
	req.Node.ApiKey = cfg.ControllerConfig.NodeApiKey
	req.Data = data
	msg, err := json.Marshal(req)
	if err := w.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		return err
	}

	return nil
}

// ReadMsg 从服务器读取消息
func (w *WsClient) ReadMsg() {
	defer func() {
		err := w.conn.Close()
		if err != nil {
			logger.Error("error closing connection", zap.Error(err))
		}
	}()

	for {
		_, msg, err := w.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error("error reading message", zap.Error(err))
			}
			break
		}
		var msgJson WsResponse
		if err := json.Unmarshal(msg, &msgJson); err != nil {
		logger.Error("can't unmarshal json message", zap.Error(err))
		continue
	}
		switch msgJson.Status {
		case 200:
			logger.Debug("received message from server", zap.String("msg", string(msg)))
		case 401:
			logger.Fatal("API reported unauthorized")
		default:
			logger.Error("error message from server", zap.String("msg", string(msg)))
		}
	}
}

func (w *WsClient) sendNodeStatsToServer(cfg *config.Config, serverInfo _type.ServerInfoResponse) {
	// nodeInfo
	err := w.SendMsg(cfg, "upload-node-stats", info.GetNodeInfo(serverInfo))
	if err != nil {
		logger.Error("send node info to server failed!", zap.Error(err))
	}
}

func (w *WsClient) sendProxyStatsToServer(cfg *config.Config) {
	types := []string{"tcp", "udp", "http", "https", "xtcp", "stcp"}
	for _, p := range types {
		proxies, err := info.GetProxies(p)
		if err != nil {
			logger.Error("can't request proxies info", zap.Error(err))
		} else {
			for _, j := range proxies {
				err := w.SendMsg(cfg, "upload-proxy-stats", j)
				logger.Info("send proxy info to the server")
				if err != nil {
					logger.Error("send proxy info to server failed!", zap.Error(err))
				}
			}
		}
	}
}

// WsClient WebSocket客户端结构
type WsClient struct {
	addr string
	conn *websocket.Conn
}

type WsResponse struct {
	Status  int             `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type BasicRequest struct {
	Action string         `json:"action"`
	Node   NodeInfo       `json:"node"`
	Data   map[string]any `json:"data"`
}

type NodeInfo struct {
	Id     int    `json:"id"`
	ApiKey string `json:"api_key"`
}

func createContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Graceful shutdown
		shutdownChan := make(chan os.Signal, 1)
		signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)
		<-shutdownChan
		logger.Info("shutting down gracefully...")

		logger.Info("stopping workers...")
		cancel()
		logger.Info("engine stopped")

		os.Exit(0)
	}()
	return ctx, cancel
}

func main() {
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		logger.Fatal("please run as root user")
		return
	}

	cfg := config.ReadCfg()

	ctx, _ := createContext()
	if cfg.OpenGFWConfig.Enable {
		go inject.RunOpenGFW(ctx, cfg.OpenGFWConfig)
	}
	if cfg.MonitorConfig.Enable {
		go inject.RunAkileMonitor(ctx, cfg.MonitorConfig)
	}

	if cfg.ControllerConfig.Enable {
		ws := NewWebSocket()
		logger.Info("connecting to WebSocket endpoint...")
		err := ws.ConnectWsServer()
		if err != nil {
			logger.Fatal(
				"can't connect to WebSocket server",
				zap.Error(err),
			)
		} else {
			logger.Info("connect to WebSocket server successfully")
			defer func(conn *websocket.Conn) {
				err := conn.Close()
				if err != nil {
					logger.Fatal(
						"can't close WebSocket connection",
						zap.Error(err),
					)
				}
			}(ws.conn)
			go ws.ReadMsg()
			ticker := time.NewTicker(cfg.ControllerConfig.SendDuration)
			defer ticker.Stop()

			serverInfo, err := server.GetServerInfo()
			if err != nil {
				logger.Error("can't get server info", zap.Error(err))
			} else {
				ws.sendNodeStatsToServer(cfg, serverInfo)
				ws.sendProxyStatsToServer(cfg)
			}

			for range ticker.C {
				if err != nil {
					logger.Error("can't get server info", zap.Error(err))
				} else {
					ws.sendNodeStatsToServer(cfg, serverInfo)
					ws.sendProxyStatsToServer(cfg)
				}
			}
		}
	} else {
		ticker := time.NewTicker(cfg.ControllerConfig.SendDuration)
		defer ticker.Stop()
		for range ticker.C {
		}
	}
}
