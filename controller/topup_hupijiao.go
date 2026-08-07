package controller

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const hupijiaoPaymentPath = "/payment/do.html"

var (
	errHupijiaoRequestRejected = errors.New("hupijiao payment request rejected")
	hupijiaoHTTPClient         = &http.Client{Timeout: 10 * time.Second}
)

type HupijiaoPayRequest struct {
	Amount int64 `json:"amount"`
}

type hupijiaoPaymentResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	Data    struct {
		URL string `json:"url"`
	} `json:"data"`
}

func hupijiaoSign(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key != "hash" && value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + secret))
	return hex.EncodeToString(sum[:])
}

func generateHupijiaoOrderID(userID int) (string, error) {
	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("HPJ-%d-%d-%s", userID, time.Now().UnixNano(), hex.EncodeToString(randomBytes)), nil
}

func generateHupijiaoNonce() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(randomBytes), nil
}

func normalizeHupijiaoTopUpAmount(amount int64) int64 {
	if operation_setting.GetQuotaDisplayType() != operation_setting.QuotaDisplayTypeTokens {
		return amount
	}

	normalized := decimal.NewFromInt(amount).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		IntPart()
	if normalized < 1 {
		return 1
	}
	return normalized
}

func getHupijiaoCallbackURL() (string, bool) {
	base := strings.TrimRight(service.GetCallbackAddress(), "/")
	if !isHupijiaoEndpointValid(base) {
		return "", false
	}
	return base + "/api/user/hupijiao/notify", true
}

func createHupijiaoPayment(ctx context.Context, endpoint string, params map[string]string) (string, error) {
	params["hash"] = hupijiaoSign(params, setting.HupijiaoAppSecret)
	body, err := common.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode Hupijiao payment request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+hupijiaoPaymentPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create Hupijiao payment request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")

	resp, err := hupijiaoHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send Hupijiao payment request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Hupijiao payment request returned HTTP %d", resp.StatusCode)
	}

	var result hupijiaoPaymentResponse
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read Hupijiao payment response: %w", err)
	}
	if err := common.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("decode Hupijiao payment response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("%w: %s", errHupijiaoRequestRejected, strings.TrimSpace(result.ErrMsg))
	}

	paymentURL, err := url.Parse(strings.TrimSpace(result.Data.URL))
	if err != nil || paymentURL.Host == "" || (paymentURL.Scheme != "http" && paymentURL.Scheme != "https") {
		return "", errors.New("Hupijiao returned an invalid payment URL")
	}
	return paymentURL.String(), nil
}

func RequestHupijiaoPay(c *gin.Context) {
	if !isHupijiaoTopUpEnabled() {
		common.ApiErrorMsg(c, "虎皮椒易支付未配置")
		return
	}

	var req HupijiaoPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "参数错误")
		return
	}
	if req.Amount < getMinTopup() {
		common.ApiErrorMsg(c, fmt.Sprintf("充值数量不能小于 %d", getMinTopup()))
		return
	}

	userID := c.GetInt("id")
	group, err := model.GetUserGroup(userID, true)
	if err != nil {
		common.ApiErrorMsg(c, "获取用户分组失败")
		return
	}

	payMoney := decimal.NewFromFloat(getPayMoney(req.Amount, group)).Round(2)
	if payMoney.LessThan(decimal.NewFromFloat(0.01)) {
		common.ApiErrorMsg(c, "充值金额过低")
		return
	}

	amount := normalizeHupijiaoTopUpAmount(req.Amount)
	quotaToAdd, clamp := common.QuotaFromDecimalChecked(decimal.NewFromInt(amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
	if clamp != nil || quotaToAdd <= 0 {
		common.ApiErrorMsg(c, "充值数量超出允许范围")
		return
	}

	notifyURL, ok := getHupijiaoCallbackURL()
	if !ok {
		common.ApiErrorMsg(c, "支付回调地址未配置")
		return
	}

	tradeNo, err := generateHupijiaoOrderID(userID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("虎皮椒易支付生成订单号失败 user_id=%d error=%q", userID, err.Error()))
		common.ApiErrorMsg(c, "创建订单失败")
		return
	}
	nonce, err := generateHupijiaoNonce()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("虎皮椒易支付生成随机串失败 user_id=%d error=%q", userID, err.Error()))
		common.ApiErrorMsg(c, "创建订单失败")
		return
	}

	topUp := &model.TopUp{
		UserId:          userID,
		Amount:          amount,
		Money:           payMoney.InexactFloat64(),
		TradeNo:         tradeNo,
		PaymentMethod:   model.PaymentMethodHupijiao,
		PaymentProvider: model.PaymentProviderHupijiao,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("虎皮椒易支付创建充值订单失败 user_id=%d trade_no=%s error=%q", userID, tradeNo, err.Error()))
		common.ApiErrorMsg(c, "创建订单失败")
		return
	}

	title := strings.TrimSpace(common.SystemName)
	if title == "" {
		title = "New API"
	}
	paymentParams := map[string]string{
		"version":        "1.1",
		"appid":          setting.HupijiaoAppID,
		"trade_order_id": tradeNo,
		"total_fee":      payMoney.StringFixed(2),
		"title":          title + " 余额充值",
		"notify_url":     notifyURL,
		"time":           strconv.FormatInt(time.Now().Unix(), 10),
		"nonce_str":      nonce,
	}
	if isHupijiaoEndpointValid(system_setting.ServerAddress) {
		base := strings.TrimRight(system_setting.ServerAddress, "/")
		paymentParams["return_url"] = base + "/wallet?show_history=true"
		paymentParams["callback_url"] = base + "/wallet"
	}

	paymentURL, err := createHupijiaoPayment(c.Request.Context(), setting.HupijiaoEndpoint, paymentParams)
	if err != nil {
		if errors.Is(err, errHupijiaoRequestRejected) {
			_ = model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderHupijiao, common.TopUpStatusFailed)
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("虎皮椒易支付拉起支付失败 user_id=%d trade_no=%s error=%q", userID, tradeNo, err.Error()))
		common.ApiErrorMsg(c, "拉起支付失败")
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("虎皮椒易支付充值订单创建成功 user_id=%d trade_no=%s amount=%d money=%s", userID, tradeNo, req.Amount, payMoney.StringFixed(2)))
	common.ApiSuccess(c, gin.H{"url": paymentURL})
}

func HupijiaoNotify(c *gin.Context) {
	if !isHupijiaoWebhookEnabled() || c.Request.Method != http.MethodPost {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	if err := c.Request.ParseForm(); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("虎皮椒易支付回调表单解析失败 client_ip=%s error=%q", c.ClientIP(), err.Error()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	params := make(map[string]string, len(c.Request.PostForm))
	for key, values := range c.Request.PostForm {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}
	receivedHash := params["hash"]
	expectedHash := hupijiaoSign(params, setting.HupijiaoAppSecret)
	if receivedHash == "" || subtle.ConstantTimeCompare([]byte(receivedHash), []byte(expectedHash)) != 1 {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("虎皮椒易支付回调验签失败 client_ip=%s", c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	tradeNo := params["trade_order_id"]
	paidMoney, err := decimal.NewFromString(params["total_fee"])
	if params["appid"] != setting.HupijiaoAppID || params["status"] != "OD" || tradeNo == "" || err != nil || paidMoney.LessThanOrEqual(decimal.Zero) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("虎皮椒易支付回调参数无效 trade_no=%s status=%s client_ip=%s", tradeNo, params["status"], c.ClientIP()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	if err := model.RechargeHupijiao(tradeNo, paidMoney, c.ClientIP()); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("虎皮椒易支付充值处理失败 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), err.Error()))
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("虎皮椒易支付充值成功 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
	_, _ = c.Writer.Write([]byte("success"))
}
