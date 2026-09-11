package discovery

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xtccc/localsend-go/internal/models"
	"github.com/xtccc/localsend-go/internal/utils/logger"
)

// defaultDevicePort 是 LocalSend 协议默认的 HTTP(S) 服务端口
const defaultDevicePort = 53317

// GetDeviceByIP 通过指定 IP 探测对端设备信息（协议/端口/别名）。
// 依次尝试 http/https 的 /api/localsend/v2/info 接口，探测失败时回退到默认值。
// 返回的 Protocol 使用实际探测成功的协议，确保后续 prepare/upload 使用可达的协议。
func GetDeviceByIP(ip string) models.SendModel {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	for _, protocol := range []string{"http", "https"} {
		url := fmt.Sprintf("%s://%s:%d/api/localsend/v2/info", protocol, ip, defaultDevicePort)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			logger.Debugf("[GetDeviceByIP] build request failed url=%q err=%v", url, err)
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			logger.Debugf("[GetDeviceByIP] GET %s failed: %v", url, err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			logger.Debugf("[GetDeviceByIP] read body from %s failed: %v", url, readErr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			logger.Debugf("[GetDeviceByIP] non-200 from %s status=%d body=%q", url, resp.StatusCode, string(body))
			continue
		}

		var info models.BroadcastMessage
		if err := json.Unmarshal(body, &info); err != nil {
			logger.Debugf("[GetDeviceByIP] parse response from %s failed: %v body=%q", url, err, string(body))
			continue
		}

		device := models.SendModel{
			DeviceName: info.Alias,
			IP:         ip,
			Port:       info.Port,
			Protocol:   protocol, // 使用实际探测成功的协议
		}
		if device.Port == 0 {
			device.Port = defaultDevicePort
		}
		logger.Infof("[GetDeviceByIP] resolved ip=%s alias=%q protocol=%s port=%d", ip, device.DeviceName, device.Protocol, device.Port)
		return device
	}

	logger.Warnf("[GetDeviceByIP] probe failed for ip=%s, fallback to http/%d", ip, defaultDevicePort)
	return models.SendModel{
		IP:       ip,
		Port:     defaultDevicePort,
		Protocol: "http",
	}
}
