package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hupijiaoRoundTripper func(*http.Request) (*http.Response, error)

func (f hupijiaoRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestHupijiaoSignSortsAndFiltersParameters(t *testing.T) {
	actual := hupijiaoSign(map[string]string{
		"total_fee": "9.90",
		"time":      "1700000000",
		"nonce_str": "abc",
		"appid":     "app",
		"empty":     "",
		"hash":      "ignored",
	}, "secret")

	assert.Equal(t, "6998ecf9a765fd777eae86a8a8d73f4d", actual)
}

func TestCreateHupijiaoPaymentPostsSignedJSONAndReturnsCheckoutURL(t *testing.T) {
	originalSecret := setting.HupijiaoAppSecret
	setting.HupijiaoAppSecret = "test-secret"
	t.Cleanup(func() {
		setting.HupijiaoAppSecret = originalSecret
	})

	originalClient := hupijiaoHTTPClient
	hupijiaoHTTPClient = &http.Client{Transport: hupijiaoRoundTripper(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, hupijiaoPaymentPath, r.URL.Path)
		require.Equal(t, "application/json;charset=UTF-8", r.Header.Get("Content-Type"))

		var requestParams map[string]string
		require.NoError(t, common.DecodeJson(r.Body, &requestParams))
		assert.Equal(t, "1.1", requestParams["version"])
		assert.Equal(t, "app-123", requestParams["appid"])
		assert.Equal(t, "order-123", requestParams["trade_order_id"])
		assert.Equal(t, "9.90", requestParams["total_fee"])
		assert.Equal(t, "32-char-nonce", requestParams["nonce_str"])
		assert.Equal(t, hupijiaoSign(requestParams, "test-secret"), requestParams["hash"])

		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(
				`{"errcode":0,"errmsg":"success","data":{"url":"https://checkout.example.com/pay/123"}}`,
			)),
			Header: make(http.Header),
		}, nil
	})}
	t.Cleanup(func() {
		hupijiaoHTTPClient = originalClient
	})

	url, err := createHupijiaoPayment(context.Background(), "https://api.dpweixin.com", map[string]string{
		"version":        "1.1",
		"appid":          "app-123",
		"trade_order_id": "order-123",
		"total_fee":      "9.90",
		"title":          "Balance topup",
		"notify_url":     "https://merchant.example.com/api/user/hupijiao/notify",
		"time":           "1700000000",
		"nonce_str":      "32-char-nonce",
	})

	require.NoError(t, err)
	assert.Equal(t, "https://checkout.example.com/pay/123", url)
}

func TestCreateHupijiaoPaymentRejectsGatewayBusinessError(t *testing.T) {
	originalSecret := setting.HupijiaoAppSecret
	setting.HupijiaoAppSecret = "test-secret"
	t.Cleanup(func() {
		setting.HupijiaoAppSecret = originalSecret
	})

	originalClient := hupijiaoHTTPClient
	hupijiaoHTTPClient = &http.Client{Transport: hupijiaoRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"errcode":1,"errmsg":"signature rejected"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	t.Cleanup(func() {
		hupijiaoHTTPClient = originalClient
	})

	_, err := createHupijiaoPayment(context.Background(), "https://api.dpweixin.com", map[string]string{
		"appid":     "app-123",
		"time":      "1700000000",
		"nonce_str": "nonce",
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, errHupijiaoRequestRejected))
}
