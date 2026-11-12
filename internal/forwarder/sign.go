package forwarder

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
)

// GenSign 计算飞书自定义机器人「签名校验」要求的 sign。
//
// 飞书的算法是：以 "timestamp\n<secret>" 作为 HMAC-SHA256 的密钥，对空内容
// （注意是空字节，不是 body）做摘要，再做 base64。也就是说 sign 只随时间戳变化，
// 与消息内容无关，必须每个请求现算——这正是 Alertmanager 自己做不了的地方。
//
// secret 为空表示机器人没开加签，返回空串，调用方据此不加 timestamp/sign。
func GenSign(secret string, timestamp int64) (string, error) {
	if secret == "" {
		return "", nil
	}

	// 时间戳按十进制拼到 secret 前面，中间必须有换行
	stringToSign := strconv.FormatInt(timestamp, 10) + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	// 待签内容为空：飞书校验的就是空 body 的摘要
	if _, err := mac.Write(nil); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}
