package controller

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"genspark2api/common"
	"genspark2api/common/config"
	"genspark2api/model"
	"github.com/Danny-Dasilva/CycleTLS/cycletls"
	"github.com/gin-gonic/gin"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

const (
	baseURL          = "https://www.genspark.ai"
	apiEndpoint      = baseURL + "/api/copilot/ask"
	deleteEndpoint   = baseURL + "/api/project/delete?project_id=%s"
	uploadEndpoint   = baseURL + "/api/get_upload_personal_image_url"
	chatType         = "COPILOT_MOA_CHAT"
	responseIDFormat = "chatcmpl-%s"
)

type OpenAIChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type OpenAIChatCompletionRequest struct {
	Messages []OpenAIChatMessage
	Model    string
}

func fetchImageAsBase64(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

func processMessages(c *gin.Context, cookie string, messages []model.OpenAIChatMessage) {
	client := cycletls.Init()

	for i, message := range messages {
		if contentArray, ok := message.Content.([]interface{}); ok {
			for j, content := range contentArray {
				if contentMap, ok := content.(map[string]interface{}); ok {
					if contentType, ok := contentMap["type"].(string); ok && contentType == "image_url" {
						if imageMap, ok := contentMap["image_url"].(map[string]interface{}); ok {
							if url, ok := imageMap["url"].(string); ok {
								processUrl(client, cookie, url, imageMap, j, contentArray)
							}
						}
					}
				}
			}
			messages[i].Content = contentArray
		}
	}
}
func processUrl(client cycletls.CycleTLS, cookie string, url string, imageMap map[string]interface{}, index int, contentArray []interface{}) {
	// 判断是否为URL
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		// 下载文件
		bytes, err := fetchImageBytes(url)
		if err != nil {
			fmt.Printf("下载文件失败: %v\n", err)
			return
		}

		processBytes(client, cookie, bytes, imageMap, index, contentArray)
	} else {
		// 尝试解析base64
		var bytes []byte
		var err error

		// 处理可能包含 data:image/ 前缀的base64
		base64Str := url
		if strings.Contains(url, ";base64,") {
			base64Str = strings.Split(url, ";base64,")[1]
		}

		bytes, err = base64.StdEncoding.DecodeString(base64Str)
		if err != nil {
			fmt.Printf("base64解码失败: %v\n", err)
			return
		}

		processBytes(client, cookie, bytes, imageMap, index, contentArray)
	}
}

func processBytes(client cycletls.CycleTLS, cookie string, bytes []byte, imageMap map[string]interface{}, index int, contentArray []interface{}) {
	// 检查是否为图片类型
	contentType := http.DetectContentType(bytes)
	if strings.HasPrefix(contentType, "image/") {
		// 是图片类型，转换为base64
		base64Data := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(bytes)
		imageMap["url"] = base64Data
	} else {
		response, err := makeGetUploadUrlRequest(client, cookie)
		if err != nil {
			fmt.Printf("makeUploadRequest ERR: %v\n", err)
			return
		}

		var jsonResponse map[string]interface{}
		if err := json.Unmarshal([]byte(response.Body), &jsonResponse); err != nil {
			fmt.Printf("Unmarshal ERR: %v\n", err)
			return
		}

		uploadImageUrl, ok := jsonResponse["data"].(map[string]interface{})["upload_image_url"].(string)
		privateStorageUrl, ok := jsonResponse["data"].(map[string]interface{})["private_storage_url"].(string)

		if !ok {
			fmt.Println("Failed to extract upload_image_url")
			return
		}

		// 发送OPTIONS预检请求
		//_, err = makeOptionsRequest(client, uploadImageUrl)
		//if err != nil {
		//	return
		//}
		// 上传文件
		_, err = makeUploadRequest(client, uploadImageUrl, bytes)
		if err != nil {
			fmt.Printf("makeUploadRequest ERR: %v\n", err)
			return
		}
		//fmt.Println(resp)

		// 创建新的 private_file 格式的内容
		privateFile := map[string]interface{}{
			"type": "private_file",
			"private_file": map[string]interface{}{
				"name":                "file", // 你可能需要从原始文件名或其他地方获取
				"type":                contentType,
				"size":                len(bytes),
				"ext":                 strings.Split(contentType, "/")[1], // 简单处理，可能需要更复杂的逻辑
				"private_storage_url": privateStorageUrl,
			},
		}

		// 替换数组中的元素
		contentArray[index] = privateFile
	}
}

// 获取文件字节数组的函数
func fetchImageBytes(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}

func createRequestBody(c *gin.Context, cookie string, openAIReq *model.OpenAIChatCompletionRequest) map[string]interface{} {
	// 处理消息中的图像 URL
	processMessages(c, cookie, openAIReq.Messages)

	// 创建请求体
	return map[string]interface{}{
		"type":                 chatType,
		"current_query_string": "type=chat",
		"messages":             openAIReq.Messages,
		//"user_s_input":         openAIReq.Messages[len(openAIReq.Messages)-1].Content,
		"action_params": map[string]interface{}{},
		"extra_data": map[string]interface{}{
			"models":                 []string{openAIReq.Model},
			"run_with_another_model": false,
			"writingContent":         nil,
		},
	}
}

// createStreamResponse 创建流式响应
func createStreamResponse(responseId, modelName string, delta model.OpenAIDelta, finishReason *string) model.OpenAIChatCompletionResponse {
	return model.OpenAIChatCompletionResponse{
		ID:      responseId,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.OpenAIChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
	}
}

// handleStreamResponse 处理流式响应
func handleStreamResponse(c *gin.Context, reader *bufio.Reader, responseId, cookie, model string) bool {

	var projectId string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return false
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, ok := event["type"].(string)
		if !ok {
			continue
		}

		switch eventType {
		case "project_start":
			projectId, _ = event["id"].(string)
		case "message_field_delta":
			if err := handleMessageFieldDelta(c, event, responseId, model); err != nil {
				return false
			}
		case "message_result":
			// 删除临时会话
			if config.AutoDelChat == 1 {
				go func() {
					c := cycletls.Init()
					makeDeleteRequest(c, cookie, projectId)
				}()
			}
			return handleMessageResult(c, responseId, model)
		}
	}
	return false
}

// handleMessageFieldDelta 处理消息字段增量
func handleMessageFieldDelta(c *gin.Context, event map[string]interface{}, responseId, modelName string) error {
	fieldName, ok := event["field_name"].(string)
	if !ok || fieldName != "session_state.answer" {
		return nil
	}

	delta, ok := event["delta"].(string)

	if !ok {
		return nil
	}

	streamResp := createStreamResponse(responseId, modelName, model.OpenAIDelta{Content: delta, Role: "assistant"}, nil)
	return sendSSEvent(c, streamResp)
}

// handleMessageResult 处理消息结果
func handleMessageResult(c *gin.Context, responseId, modelName string) bool {
	finishReason := "stop"

	streamResp := createStreamResponse(responseId, modelName, model.OpenAIDelta{}, &finishReason)
	if err := sendSSEvent(c, streamResp); err != nil {
		return false
	}
	c.SSEvent("", " [DONE]")
	return false
}

// sendSSEvent 发送SSE事件
func sendSSEvent(c *gin.Context, response model.OpenAIChatCompletionResponse) error {
	jsonResp, err := json.Marshal(response)
	if err != nil {
		return err
	}
	c.SSEvent("", " "+string(jsonResp))
	c.Writer.Flush()
	return nil
}

// makeRequest 发送HTTP请求
func makeRequest(client cycletls.CycleTLS, jsonData []byte, cookie string, isStream bool) (cycletls.Response, error) {
	accept := "application/json"
	if isStream {
		accept = "text/event-stream"
	}

	return client.Do(apiEndpoint, cycletls.Options{
		Timeout: 10 * 60 * 60,
		Body:    string(jsonData),
		Method:  "POST",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       accept,
			"Origin":       baseURL,
			"Referer":      baseURL + "/",
			"Cookie":       cookie,
		},
	}, "POST")
}

func makeDeleteRequest(client cycletls.CycleTLS, cookie, projectId string) (cycletls.Response, error) {
	accept := "application/json"

	return client.Do(fmt.Sprintf(deleteEndpoint, projectId), cycletls.Options{
		Timeout: 10 * 60 * 60,
		Method:  "GET",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       accept,
			"Origin":       baseURL,
			"Referer":      baseURL + "/",
			"Cookie":       cookie,
		},
	}, "GET")
}

func makeGetUploadUrlRequest(client cycletls.CycleTLS, cookie string) (cycletls.Response, error) {

	accept := "*/*"

	return client.Do(fmt.Sprintf(uploadEndpoint), cycletls.Options{
		Timeout: 10 * 60 * 60,
		Method:  "GET",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       accept,
			"Origin":       baseURL,
			"Referer":      baseURL + "/",
			"Cookie":       cookie,
		},
	}, "GET")
}

func makeOptionsRequest(client cycletls.CycleTLS, uploadUrl string) (cycletls.Response, error) {
	return client.Do(uploadUrl, cycletls.Options{
		Method: "OPTIONS",
		Headers: map[string]string{
			"Accept":                         "*/*",
			"Access-Control-Request-Headers": "x-ms-blob-type",
			"Access-Control-Request-Method":  "PUT",
			"Origin":                         "https://www.genspark.ai",
			"Sec-Fetch-Dest":                 "empty",
			"Sec-Fetch-Mode":                 "cors",
			"Sec-Fetch-Site":                 "cross-site",
		},
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}, "OPTIONS")
}

func makeUploadRequest(client cycletls.CycleTLS, uploadUrl string, fileBytes []byte) (cycletls.Response, error) {
	return client.Do(uploadUrl, cycletls.Options{
		Timeout: 30,
		Method:  "PUT",
		Body:    string(fileBytes),
		Headers: map[string]string{
			"Accept":         "*/*",
			"x-ms-blob-type": "BlockBlob",
			"Content-Type":   "application/octet-stream",
			"Content-Length": fmt.Sprintf("%d", len(fileBytes)),
			"Origin":         "https://www.genspark.ai",
			"Sec-Fetch-Dest": "empty",
			"Sec-Fetch-Mode": "cors",
			"Sec-Fetch-Site": "cross-site",
		},
		Ja3:       "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-17513,29-23-24,0",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}, "PUT")
}

// ChatForOpenAI 处理OpenAI聊天请求
func ChatForOpenAI(c *gin.Context) {
	var openAIReq model.OpenAIChatCompletionRequest
	if err := c.BindJSON(&openAIReq); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	cookie, err := common.RandomElement(config.GSCookies)
	if err != nil {
		return
	}

	requestBody := createRequestBody(c, cookie, &openAIReq)
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to marshal request body"})
		return
	}

	client := cycletls.Init()

	if openAIReq.Stream {
		handleStreamRequest(c, client, cookie, jsonData, openAIReq.Model)
	} else {
		handleNonStreamRequest(c, client, cookie, jsonData, openAIReq.Model)
	}

}

// handleStreamRequest 处理流式请求
func handleStreamRequest(c *gin.Context, client cycletls.CycleTLS, cookie string, jsonData []byte, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	responseId := fmt.Sprintf(responseIDFormat, time.Now().Format("20060102150405"))

	c.Stream(func(w io.Writer) bool {
		response, err := makeRequest(client, jsonData, cookie, true)
		if err != nil {
			return false
		}

		reader := bufio.NewReader(strings.NewReader(response.Body))
		return handleStreamResponse(c, reader, responseId, cookie, model)
	})
}

// handleNonStreamRequest 处理非流式请求
func handleNonStreamRequest(c *gin.Context, client cycletls.CycleTLS, cookie string, jsonData []byte, modelName string) {
	response, err := makeRequest(client, jsonData, cookie, false)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	reader := strings.NewReader(response.Body)
	scanner := bufio.NewScanner(reader)

	var content string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var parsedResponse struct {
				Type      string `json:"type"`
				FieldName string `json:"field_name"`
				Content   string `json:"content"`
			}
			if err := json.Unmarshal([]byte(data), &parsedResponse); err != nil {
				continue
			}
			if parsedResponse.Type == "message_result" {
				content = parsedResponse.Content
				break
			}
		}
	}

	if content == "" {
		c.JSON(500, gin.H{"error": "No valid response content"})
		return
	}

	finishReason := "stop"
	// 创建并返回 OpenAIChatCompletionResponse 结构
	resp := model.OpenAIChatCompletionResponse{
		ID:      fmt.Sprintf(responseIDFormat, time.Now().Format("20060102150405")),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.OpenAIChoice{
			{
				Message: model.OpenAIMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: &finishReason,
			},
		},
	}

	c.JSON(200, resp)
}

func OpenaiModels(c *gin.Context) {
	var modelsResp []string

	modelsResp = common.DefaultOpenaiModelList

	var openaiModelListResponse model.OpenaiModelListResponse
	var openaiModelResponse []model.OpenaiModelResponse
	openaiModelListResponse.Object = "list"

	for _, modelResp := range modelsResp {
		openaiModelResponse = append(openaiModelResponse, model.OpenaiModelResponse{
			ID:     modelResp,
			Object: "model",
		})
	}
	openaiModelListResponse.Data = openaiModelResponse
	c.JSON(http.StatusOK, openaiModelListResponse)
	return
}
