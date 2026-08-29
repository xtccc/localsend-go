package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/meowrain/localsend-go/internal/models"

	"github.com/meowrain/localsend-go/internal/utils/clipboard"
	"github.com/meowrain/localsend-go/internal/utils/logger"
	"github.com/schollz/progressbar/v3"
)

var (
	sessionIDCounter = 0
	sessionMutex     sync.Mutex
	fileNames        = make(map[string]string) // 用于保存文件名
)

func PrepareReceive(w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	logger.Infof("[PrepareReceive] incoming from %s method=%s url=%q", remoteAddr, r.Method, r.URL.String())
	// 读取原始 body 便于调试
	rawBody, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		logger.Errorf("[PrepareReceive] read body failed from %s: %v", remoteAddr, readErr)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	logger.Debugf("[PrepareReceive] raw body from %s: %s", remoteAddr, string(rawBody))
	// 重新放回 body 供 decoder 使用（已读完，直接从 rawBody 解码）
	var req models.PrepareReceiveRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		logger.Errorf("[PrepareReceive] json decode failed from %s: %v body=%q", remoteAddr, err, string(rawBody))
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	logger.Infof("[PrepareReceive] received from alias=%q deviceModel=%q fingerprint=%q ip=%s files=%d", req.Info.Alias, req.Info.DeviceModel, req.Info.Fingerprint, remoteAddr, len(req.Files))
	for fid, finfo := range req.Files {
		logger.Debugf("[PrepareReceive] file entry id=%q fileName=%q size=%d fileType=%q sha256=%q previewLen=%d", fid, finfo.FileName, finfo.Size, finfo.FileType, finfo.SHA256, len(finfo.Preview))
	}

	sessionMutex.Lock()
	sessionIDCounter++
	sessionID := fmt.Sprintf("session-%d", sessionIDCounter)
	sessionMutex.Unlock()
	logger.Infof("[PrepareReceive] generated sessionId=%q for %s", sessionID, remoteAddr)

	files := make(map[string]string)
	for fileID, fileInfo := range req.Files {
		token := fmt.Sprintf("token-%s", fileID)
		files[fileID] = token
		logger.Debugf("[PrepareReceive] mapping fileId=%q -> token=%q (fileName=%q)", fileID, token, fileInfo.FileName)

		// 保存文件名
		fileNames[fileID] = fileInfo.FileName

		if strings.HasSuffix(fileInfo.FileName, ".txt") {
			logger.Success("TXT file content preview:", string(fileInfo.Preview))
			clipboard.WriteToClipBoard(fileInfo.Preview)
		}
	}

	resp := models.PrepareReceiveResponse{
		SessionID: sessionID,
		Files:     files,
	}
	respJson, _ := json.Marshal(resp)
	logger.Infof("[PrepareReceive] respond to %s sessionId=%q files=%d json=%s", remoteAddr, sessionID, len(files), string(respJson))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Errorf("[PrepareReceive] encode response failed: %v", err)
	}
}

func ReceiveHandler(w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	rawQuery := r.URL.RawQuery
	encodedURL := r.URL.String()
	logger.Infof("[ReceiveHandler] incoming upload from %s method=%s url=%q rawQuery=%q", remoteAddr, r.Method, encodedURL, rawQuery)
	logger.Debugf("[ReceiveHandler] headers=%v contentLength=%d", r.Header, r.ContentLength)
	sessionID := r.URL.Query().Get("sessionId")
	fileID := r.URL.Query().Get("fileId")
	token := r.URL.Query().Get("token")
	logger.Debugf("[ReceiveHandler] parsed query sessionId=%q fileId=%q token=%q (fileId len=%d token len=%d)", sessionID, fileID, token, len(fileID), len(token))
	// 额外：打印原始 query 中未解码的值以排查编码问题
	for k, vals := range r.URL.Query() {
		for _, v := range vals {
			logger.Debugf("[ReceiveHandler] query param %q=%q", k, v)
		}
	}

	// 验证请求参数
	if sessionID == "" || fileID == "" || token == "" {
		logger.Warnf("[ReceiveHandler] missing params from %s sessionId=%q fileId=%q token=%q rawQuery=%q", remoteAddr, sessionID, fileID, token, rawQuery)
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	// 校验 token（当前实现为 token-<fileID>，若不匹配则提示，保留兼容但打日志）
	expectedToken := fmt.Sprintf("token-%s", fileID)
	if token != expectedToken {
		logger.Warnf("[ReceiveHandler] token mismatch from %s fileId=%q got token=%q expected=%q rawURL=%q", remoteAddr, fileID, token, expectedToken, encodedURL)
		// 暂不直接拒绝，仅告警；若需严格校验可在此返回 403
		// http.Error(w, "Invalid token or IP address", http.StatusForbidden)
		// return
	} else {
		logger.Debugf("[ReceiveHandler] token verified ok fileId=%q", fileID)
	}

	// 使用 fileID 获取文件名
	fileName, ok := fileNames[fileID]
	if !ok {
		logger.Warnf("[ReceiveHandler] Invalid file ID=%q from %s known keys=%v", fileID, remoteAddr, getFileNamesKeys())
		http.Error(w, "Invalid file ID", http.StatusBadRequest)
		return
	}
	logger.Infof("[ReceiveHandler] resolved fileId=%q -> fileName=%q sessionId=%q", fileID, fileName, sessionID)

	// 生成文件路径，保留文件扩展名
	filePath := filepath.Join("uploads", fileName)
	logger.Debugf("[ReceiveHandler] target filePath=%q dir=%q", filePath, filepath.Dir(filePath))
	// 创建文件夹（如果不存在）
	dir := filepath.Dir(filePath)
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		logger.Errorf("[ReceiveHandler] mkdir failed dir=%q err=%v", dir, err)
		http.Error(w, "Failed to create directory", http.StatusInternalServerError)
		return
	}
	// 创建文件
	file, err := os.Create(filePath)
	if err != nil {
		logger.Errorf("[ReceiveHandler] create file failed path=%q err=%v", filePath, err)
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	logger.Debugf("[ReceiveHandler] file created %q ready to receive", filePath)

	// 创建一个 context 来处理请求取消
	ctx := r.Context()

	// 创建文件后，获取文件大小
	contentLength := r.ContentLength

	// 创建进度条
	bar := progressbar.NewOptions64(
		contentLength,
		progressbar.OptionSetDescription(fmt.Sprintf("下载 %s", fileName)),
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

	buffer := make([]byte, 2*1024*1024) // 2MB 缓冲区

	// 使用 channel 来处理传输完成或取消
	done := make(chan error, 1)

	go func() {
		for {
			n, err := r.Body.Read(buffer)
			if err != nil && err != io.EOF {
				done <- fmt.Errorf("Read file failed: %w", err)
				return
			}
			if n == 0 {
				done <- nil
				return
			}

			_, err = file.Write(buffer[:n])
			if err != nil {
				done <- fmt.Errorf("Write file failed: %w", err)
				return
			}

			bar.Add(n)
		}
	}()

	logger.Debugf("[ReceiveHandler] waiting for transfer fileId=%q from %s", fileID, remoteAddr)
	// 等待传输完成或取消
	select {
	case err := <-done:
		if err != nil {
			logger.Errorf("[ReceiveHandler] transfer error fileId=%q filePath=%q err=%v", fileID, filePath, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			// 删除未完成的文件
			os.Remove(filePath)
			return
		}
		logger.Debugf("[ReceiveHandler] transfer done fileId=%q filePath=%q", fileID, filePath)
	case <-ctx.Done():
		// 请求被取消
		logger.Infof("[ReceiveHandler] Transfer canceled by client fileId=%q from %s", fileID, remoteAddr)
		// 删除未完成的文件
		os.Remove(filePath)
		// 关闭连接
		if conn, ok := w.(http.CloseNotifier); ok {
			conn.CloseNotify()
		}
		return
	}

	logger.Debugf("[ReceiveHandler] transfer done filePath=%q size=%d from %s", filePath, contentLength, remoteAddr)
	logger.Success("File saved to:", filePath)
	w.WriteHeader(http.StatusOK)
}

func getFileNamesKeys() []string {
	keys := make([]string, 0, len(fileNames))
	for k := range fileNames {
		keys = append(keys, k)
	}
	return keys
}
