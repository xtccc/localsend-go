package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/meowrain/localsend-go/internal/discovery"
	"github.com/meowrain/localsend-go/internal/discovery/shared"
	"github.com/meowrain/localsend-go/internal/models"
	"github.com/meowrain/localsend-go/internal/tui"
	"github.com/meowrain/localsend-go/internal/utils/logger"
	"github.com/meowrain/localsend-go/internal/utils/sha256"
	"github.com/schollz/progressbar/v3"
)

// SendFileToOtherDevicePrepare 函数
func SendFileToOtherDevicePrepare(device models.SendModel, path string) (*models.PrepareReceiveResponse, error) {
	ip := device.IP
	protocol := device.Protocol
	port := device.Port
	if protocol == "" {
		protocol = "https"
	}
	if port == 0 {
		port = 53317
	}
	logger.Debugf("[Prepare] start ip=%s protocol=%s port=%d path=%q", ip, protocol, port, path)
	// 准备所有文件的元数据
	files := make(map[string]models.FileInfo)
	err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			logger.Errorf("[Prepare][Walk] error at %q: %v", filePath, err)
			return err
		}
		if info == nil {
			logger.Warnf("[Prepare][Walk] nil info for %q", filePath)
			return nil
		}
		logger.Debugf("[Prepare][Walk] visit filePath=%q isDir=%v size=%d name=%q", filePath, info.IsDir(), info.Size(), info.Name())
		if !info.IsDir() {
			sha256Hash, err := sha256.CalculateSHA256(filePath)
			if err != nil {
				logger.Errorf("[Prepare][Walk] sha256 failed for %q: %v", filePath, err)
				return fmt.Errorf("error calculating SHA256 hash: %w", err)
			}
			fileMetadata := models.FileInfo{
				ID:       info.Name(), // 使用文件名作为 ID
				FileName: info.Name(),
				Size:     info.Size(),
				FileType: filepath.Ext(filePath),
				SHA256:   sha256Hash,
			}
			if _, exists := files[fileMetadata.ID]; exists {
				logger.Warnf("[Prepare][Walk] duplicate fileId=%q from %q will overwrite previous entry", fileMetadata.ID, filePath)
			}
			files[fileMetadata.ID] = fileMetadata
			logger.Debugf("[Prepare][Walk] added fileId=%q fileName=%q size=%d sha256=%s", fileMetadata.ID, fileMetadata.FileName, fileMetadata.Size, fileMetadata.SHA256)
		}
		return nil
	})
	if err != nil {
		logger.Errorf("[Prepare] Walk failed for path=%q: %v", path, err)
		return nil, fmt.Errorf("error walking the path: %w", err)
	}
	logger.Infof("[Prepare] Walk done path=%q discovered %d file(s)", path, len(files))
	for fid, finfo := range files {
		logger.Debugf("[Prepare] file entry id=%q fileName=%q size=%d", fid, finfo.FileName, finfo.Size)
	}

	// 创建并填充 PrepareReceiveRequest 结构体
	request := models.PrepareReceiveRequest{
		Info: models.Info{
			Alias:       shared.Message.Alias,
			Version:     shared.Message.Version,
			DeviceModel: shared.Message.DeviceModel,
			DeviceType:  shared.Message.DeviceType,
			Fingerprint: shared.Message.Fingerprint,
			Port:        shared.Message.Port,
			Protocol:    shared.Message.Protocol,
			Download:    shared.Message.Download,
		},
		Files: files,
	}

	// 将请求结构体编码为JSON
	requestJson, err := json.Marshal(request)
	if err != nil {
		logger.Errorf("[Prepare] json.Marshal failed: %v", err)
		return nil, fmt.Errorf("error encoding request to JSON: %w", err)
	}
	logger.Debugf("[Prepare] request json=%s", string(requestJson))
	logger.Debugf("[Prepare] request info alias=%q fingerprint=%q deviceModel=%q protocol=%q port=%d files=%d", request.Info.Alias, request.Info.Fingerprint, request.Info.DeviceModel, request.Info.Protocol, request.Info.Port, len(request.Files))

	// 发送POST请求（根据对端广播的 protocol/port 动态选择，避免 http→https 错配）
	url := fmt.Sprintf("%s://%s:%d/api/localsend/v2/prepare-upload", protocol, ip, port)
	logger.Infof("[Prepare] POST %s to %s (files=%d) protocol=%s port=%d", url, ip, len(files), protocol, port)
	client := &http.Client{
		Timeout: 60 * time.Second, // 传输超时
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // 忽略TLS
			},
		},
	}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(requestJson))
	if err != nil {
		logger.Errorf("[Prepare] POST failed url=%s err=%v", url, err)
		return nil, fmt.Errorf("error sending POST request: %w", err)
	}
	defer resp.Body.Close()
	logger.Debugf("[Prepare] response status=%d %s from %s", resp.StatusCode, http.StatusText(resp.StatusCode), url)

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Warnf("[Prepare] non-200 response status=%d body=%q headers=%v", resp.StatusCode, string(bodyBytes), resp.Header)
		switch resp.StatusCode {
		case 204:
			return nil, fmt.Errorf("finished (No file transfer needed)")
		case 400:
			return nil, fmt.Errorf("invalid body")
		case 403:
			return nil, fmt.Errorf("rejected")
		case 500:
			return nil, fmt.Errorf("unknown error by receiver")
		}
		return nil, fmt.Errorf("failed to send metadata: received status code %d body=%q", resp.StatusCode, string(bodyBytes))
	}

	// 解码响应JSON为PrepareReceiveResponse结构体
	var prepareReceiveResponse models.PrepareReceiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&prepareReceiveResponse); err != nil {
		logger.Errorf("[Prepare] decode response failed: %v", err)
		return nil, fmt.Errorf("error decoding response JSON: %w", err)
	}
	logger.Infof("[Prepare] success sessionId=%q files=%d from %s", prepareReceiveResponse.SessionID, len(prepareReceiveResponse.Files), ip)
	for fid, token := range prepareReceiveResponse.Files {
		logger.Debugf("[Prepare] response fileId=%q token=%q (len=%d)", fid, token, len(token))
	}

	return &prepareReceiveResponse, nil
}

// uploadFile 函数
func uploadFile(ctx context.Context, device models.SendModel, sessionId, fileId, token, filePath string) error {
	ip := device.IP
	protocol := device.Protocol
	port := device.Port
	if protocol == "" {
		protocol = "https"
	}
	if port == 0 {
		port = 53317
	}
	logger.Infof("[Upload] start ip=%s protocol=%s port=%d sessionId=%q fileId=%q token=%q filePath=%q", ip, protocol, port, sessionId, fileId, token, filePath)
	// 诊断：记录原始 fileId/token 是否含非ASCII/特殊字符
	logger.Debugf("[Upload] fileId raw=%q len=%d token raw=%q len=%d sessionId=%q", fileId, len(fileId), token, len(token), sessionId)
	// 打开要发送的文件
	file, err := os.Open(filePath)
	if err != nil {
		logger.Errorf("[Upload] open failed filePath=%q err=%v", filePath, err)
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	// 获取文件大小用于进度条
	fileInfo, err := file.Stat()
	if err != nil {
		logger.Errorf("[Upload] stat failed filePath=%q err=%v", filePath, err)
		return fmt.Errorf("error getting file info: %w", err)
	}
	fileSize := fileInfo.Size()
	logger.Debugf("[Upload] file stat size=%d modTime=%v filePath=%q", fileSize, fileInfo.ModTime(), filePath)

	// 创建进度条
	bar := progressbar.NewOptions64(
		fileSize,
		progressbar.OptionSetDescription(fmt.Sprintf("上传 %s", filepath.Base(filePath))),
		progressbar.OptionSetWidth(15),
		progressbar.OptionShowBytes(true),
		progressbar.OptionThrottle(time.Second), // 降低刷新频率，减少闪烁
		progressbar.OptionShowCount(),
		progressbar.OptionClearOnFinish(), // 完成时清除进度条
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionSetPredictTime(true), // 预测剩余时间
		progressbar.OptionFullWidth(),          // 使用全宽显示
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█", // 使用实心方块
			SaucerHead:    "█",
			SaucerPadding: "░", // 使用灰色方块作为背景
			BarStart:      "|",
			BarEnd:        "|",
		}),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
	)

	// 构建文件上传的 URL（做 URL 编码以支持中文/特殊字符，协议/端口随对端）
	escapedSessionId := url.QueryEscape(sessionId)
	escapedFileId := url.QueryEscape(fileId)
	escapedToken := url.QueryEscape(token)
	// 调试：对比编码前后
	rawURL := fmt.Sprintf("%s://%s:%d/api/localsend/v2/upload?sessionId=%s&fileId=%s&token=%s", protocol, ip, port, sessionId, fileId, token)
	uploadURL := fmt.Sprintf("%s://%s:%d/api/localsend/v2/upload?sessionId=%s&fileId=%s&token=%s",
		protocol, ip, port, escapedSessionId, escapedFileId, escapedToken)
	logger.Debugf("[Upload] raw uploadURL=%q", rawURL)
	logger.Infof("[Upload] encoded uploadURL=%q (fileId encoded %q -> %q)", uploadURL, fileId, escapedFileId)
	if rawURL != uploadURL {
		logger.Warnf("[Upload] URL was escaped due to special chars; raw fileId=%q token=%q", fileId, token)
	}

	// 使用 pipe 来避免将整个文件加载到内存中
	pr, pw := io.Pipe()

	// 创建一个错误通道来传递上传过程中的错误
	uploadErr := make(chan error, 1)

	go func() {
		defer pw.Close()
		// 在新的 goroutine 中写入文件数据
		_, err := io.Copy(io.MultiWriter(pw, bar), file)
		if err != nil {
			uploadErr <- err
			return
		}
	}()

	// 创建带有 TLS 配置的 HTTP 客户端
	client := &http.Client{
		Timeout: 30 * time.Minute,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // 跳过证书验证
			},
			MaxIdleConns:       100,
			IdleConnTimeout:    90 * time.Second,
			DisableCompression: true,
		},
	}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, pr)
	if err != nil {
		logger.Errorf("[Upload] NewRequest failed url=%q err=%v", uploadURL, err)
		return fmt.Errorf("error creating POST request: %w", err)
	}

	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = fileSize
	logger.Debugf("[Upload] request headers Content-Length=%d Content-Type=%s URL=%q", req.ContentLength, req.Header.Get("Content-Type"), uploadURL)

	// 使用自定义客户端发送请求，而不是 http.DefaultClient
	resp, err := client.Do(req)
	logger.Debugf("[Upload] client.Do returned err=%v resp=%v", err, resp)

	// 检查是否被取消
	select {
	case <-ctx.Done():
		logger.Warnf("[Upload] transfer canceled by context sessionId=%q fileId=%q", sessionId, fileId)
		return fmt.Errorf("传输已取消")
	case err := <-uploadErr:
		if err != nil {
			logger.Errorf("[Upload] pipe upload error sessionId=%q fileId=%q err=%v", sessionId, fileId, err)
			return fmt.Errorf("上传出错: %w", err)
		}
		logger.Debugf("[Upload] pipe copy done without error sessionId=%q fileId=%q", sessionId, fileId)
	default:
		if err != nil {
			logger.Errorf("[Upload] client.Do failed url=%q err=%v", uploadURL, err)
			return fmt.Errorf("error sending file upload request: %w", err)
		}
	}
	if resp == nil {
		logger.Errorf("[Upload] nil response after client.Do url=%q", uploadURL)
		return fmt.Errorf("nil response from server")
	}
	logger.Debugf("[Upload] response status=%d %s headers=%v", resp.StatusCode, http.StatusText(resp.StatusCode), resp.Header)

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Warnf("[Upload] non-200 status=%d body=%q headers=%v url=%q fileId=%q token=%q sessionId=%q remoteIP=%s", resp.StatusCode, string(bodyBytes), resp.Header, uploadURL, fileId, token, sessionId, ip)
		switch resp.StatusCode {
		case 400:
			return fmt.Errorf("missing parameters (status 400 body=%q)", string(bodyBytes))
		case 403:
			return fmt.Errorf("invalid token or IP address (status 403 body=%q fileId=%q token=%q)", string(bodyBytes), fileId, token)
		case 409:
			return fmt.Errorf("blocked by another session (status 409 body=%q)", string(bodyBytes))
		case 500:
			return fmt.Errorf("unknown error by receiver (status 500 body=%q)", string(bodyBytes))
		}
		return fmt.Errorf("file upload failed: received status code %d body=%q", resp.StatusCode, string(bodyBytes))
	}
	// 成功时也读取 body 便于调试（通常为空）
	bodyBytes, _ := io.ReadAll(resp.Body)
	logger.Debugf("[Upload] success response body=%q", string(bodyBytes))

	fmt.Println() // 添加换行，让进度条显示更清晰
	logger.Success(fmt.Sprintf("File uploaded successfully fileId=%q filePath=%q sessionId=%q", fileId, filePath, sessionId))
	return nil
}

// SendFile 函数
// targetIP 非空时直接向指定 IP 发送，跳过设备发现与交互式选择
func SendFile(path string, targetIP string) error {
	logger.Infof("[SendFile] start path=%q targetIP=%q", path, targetIP)

	var device models.SendModel
	if targetIP != "" {
		device = discovery.GetDeviceByIP(targetIP)
		logger.Infof("[SendFile] using specified device ip=%s protocol=%s port=%d", device.IP, device.Protocol, device.Port)
	} else {
		updates := make(chan []models.SendModel)
		discovery.ListenAndStartBroadcasts(updates)
		fmt.Println("Please select a device you want to send file to:")
		selected, err := tui.SelectDevice(updates)
		if err != nil {
			logger.Errorf("[SendFile] SelectDevice failed: %v", err)
			return err
		}
		device = selected
	}

	ip := device.IP
	if device.Protocol == "" {
		device.Protocol = "https"
	}
	if device.Port == 0 {
		device.Port = 53317
	}
	logger.Infof("[SendFile] selected device ip=%s protocol=%s port=%d path=%q", ip, device.Protocol, device.Port, path)
	response, err := SendFileToOtherDevicePrepare(device, path)
	if err != nil {
		logger.Errorf("[SendFile] Prepare failed ip=%s protocol=%s port=%d path=%q err=%v", ip, device.Protocol, device.Port, path, err)
		return err
	}
	logger.Infof("[SendFile] Prepare success sessionId=%q files=%d ip=%s", response.SessionID, len(response.Files), ip)

	// 创建一个用于取消的 context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 使用共享的 HTTP 服务器来处理取消请求
	logger.Info("Registering cancel handler for session: ", response.SessionID)
	RegisterCancelHandler(response.SessionID, cancel)
	defer UnregisterCancelHandler(response.SessionID)

	// 遍历目录和子文件
	logger.Debugf("[SendFile] walking path=%q to upload", path)
	err = filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			logger.Errorf("[SendFile][Walk] error at %q: %v", filePath, err)
			return err
		}
		if info == nil {
			logger.Warnf("[SendFile][Walk] nil info for %q", filePath)
			return nil
		}
		logger.Debugf("[SendFile][Walk] visit %q isDir=%v", filePath, info.IsDir())
		if !info.IsDir() {
			fileId := info.Name()
			logger.Debugf("[SendFile][Walk] need upload fileId=%q filePath=%q", fileId, filePath)
			token, ok := response.Files[fileId]
			if !ok {
				logger.Errorf("[SendFile] token not found for fileId=%q available keys=%v", fileId, getKeys(response.Files))
				return fmt.Errorf("token not found for file: %s", fileId)
			}
			logger.Debugf("[SendFile] found token for fileId=%q token=%q len=%d", fileId, token, len(token))
			err = uploadFile(ctx, device, response.SessionID, fileId, token, filePath)
			if err != nil {
				logger.Errorf("[SendFile] uploadFile failed fileId=%q filePath=%q err=%v", fileId, filePath, err)
				return fmt.Errorf("error uploading file: %w", err)
			}
			logger.Infof("[SendFile] uploaded fileId=%q filePath=%q", fileId, filePath)
		}
		return nil
	})
	if err != nil {
		logger.Errorf("[SendFile] Walk/upload failed path=%q err=%v", path, err)
		return fmt.Errorf("error walking the path: %w", err)
	}

	logger.Success(fmt.Sprintf("[SendFile] all done path=%q sessionId=%q", path, response.SessionID))
	return nil
}

func getKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func NormalSendHandler(w http.ResponseWriter, r *http.Request) {
	logger.Info("Handling upload request...") // Debug log - request start

	// 限制表单数据大小（此处设置为 10 MB，可根据需要调整）
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, fmt.Sprintf("解析表单失败: %v", err), http.StatusBadRequest)
		return
	}

	// 获取上传的目录名 (来自前端 hidden input)
	uploadedDirName := r.FormValue("directoryName")
	logger.Debugf("directoryName from form: '%s'", uploadedDirName) // Debug log - directoryName value

	// 获取所有上传的文件
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		http.Error(w, "未上传任何文件", http.StatusBadRequest)
		return
	}

	uploadDir := "./uploads"    // 基础上传目录
	finalUploadDir := uploadDir // 默认最终上传目录

	// 如果前端传递了目录名且不为空，才创建以目录名命名的子目录
	if uploadedDirName != "" {
		finalUploadDir = filepath.Join(uploadDir, uploadedDirName)
	} else {
		logger.Debug("No directoryName provided, uploading to root uploads dir.") // Debug log - no directoryName
	}
	logger.Debugf("Final upload directory: '%s'", finalUploadDir)

	// 创建最终的上传目录（如果不存在）
	if err := os.MkdirAll(finalUploadDir, os.ModePerm); err != nil {
		http.Error(w, fmt.Sprintf("无法创建上传目录: %v", err), http.StatusInternalServerError)
		return
	}

	// 遍历所有文件进行保存
	for _, fileHeader := range files {
		// 打开上传的文件
		file, err := fileHeader.Open()
		if err != nil {
			http.Error(w, fmt.Sprintf("无法打开文件: %v", err), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		// 拼接目标路径 (使用 finalUploadDir 作为根目录)
		destPath := filepath.Join(finalUploadDir, fileHeader.Filename)
		logger.Infof("Saving file '%s' to destPath: '%s'", fileHeader.Filename, destPath) // Debug log - file dest path

		// 创建目标目录（如果不存在）
		if err := os.MkdirAll(filepath.Dir(destPath), os.ModePerm); err != nil {
			http.Error(w, fmt.Sprintf("无法创建目录: %v", err), http.StatusInternalServerError)
			return
		}

		// 创建目标文件
		dst, err := os.Create(destPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("无法创建文件: %v", err), http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		// 将上传的文件内容写入目标文件
		if _, err := io.Copy(dst, file); err != nil {
			http.Error(w, fmt.Sprintf("保存文件失败: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "文件上传成功，共计 %d 个文件，上传到目录: %s\n", len(files), finalUploadDir)
}
