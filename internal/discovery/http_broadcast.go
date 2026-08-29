package discovery

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/meowrain/localsend-go/internal/discovery/shared"
	"github.com/meowrain/localsend-go/internal/models"
	"github.com/meowrain/localsend-go/internal/utils/logger"
)

func ListenForHttpBroadCast(updates chan<- []models.SendModel) {
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	for range ticker.C {
		data, err := json.Marshal(shared.Message)
		if err != nil {
			logger.Errorf("Failed to marshal message: %v", err)
			continue
		}

		ips, err := pingScan()
		if err != nil {
			logger.Errorf("[HttpBroadcast] pingScan failed: %v", err)
			continue
		}
		logger.Debugf("[HttpBroadcast] pingScan found %d ips: %v", len(ips), ips)

		var wg sync.WaitGroup
		for _, ip := range ips {
			wg.Add(1)
			go func(ip string) {
				defer wg.Done()
				url := fmt.Sprintf("https://%s:%d/api/localsend/v2/register", ip, broadcastPort)
				req, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
				if err != nil {
					logger.Errorf("Failed to create HTTP request for %s: %v", ip, err)
					return
				}
				req.Header.Set("Content-Type", "application/json")

				client := &http.Client{
					Timeout: httpTimeout,
					Transport: &http.Transport{
						TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
					},
				}

				resp, err := client.Do(req)
				if err != nil {
					logger.Debugf("[HttpBroadcast] POST register to %s failed: %v", ip, err)
					return
				}
				defer resp.Body.Close()
				logger.Debugf("[HttpBroadcast] POST register to %s status=%d", ip, resp.StatusCode)

				body, err := io.ReadAll(resp.Body)
				if err != nil {
					logger.Errorf("[HttpBroadcast] read body from %s failed: %v", ip, err)
					return
				}
				if resp.StatusCode != http.StatusOK {
					logger.Debugf("[HttpBroadcast] non-200 from %s status=%d body=%q", ip, resp.StatusCode, string(body))
					return
				}

				var response models.BroadcastMessage
				if err := json.Unmarshal(body, &response); err != nil {
					logger.Errorf("[HttpBroadcast] parse response from %s failed: %v body=%q", ip, err, string(body))
					return
				}
				logger.Debugf("[HttpBroadcast] discovered via http %s alias=%q model=%q", ip, response.Alias, response.DeviceModel)

				// 过滤本机 IP
				if IsLocalIP(ip) {
					logger.Debugf("[HttpBroadcast] ignoring self ip %s", ip)
					return
				}
				response.LastSeen = time.Now()

				shared.DevicesMutex.Lock()
				shared.DiscoveredDevices[ip] = response
				shared.DevicesMutex.Unlock()
			}(ip)
		}

		wg.Wait()

		shared.DevicesMutex.RLock()
		devices := make([]models.SendModel, 0, len(shared.DiscoveredDevices))
		for ip, device := range shared.DiscoveredDevices {
			if IsLocalIP(ip) {
				continue
			}
			devices = append(devices, models.SendModel{
				IP:         ip,
				DeviceName: device.Alias,
				Port:       device.Port,
				Protocol:   device.Protocol,
			})
		}
		shared.DevicesMutex.RUnlock()
		logger.Debugf("[HttpBroadcast] total discovered devices %d: %v", len(devices), devices)

		select {
		case updates <- devices:
			logger.Debugf("[HttpBroadcast] sent updates %d devices", len(devices))
		default:
			logger.Debug("Updates channel is full, skipping update")
		}
	}
}
