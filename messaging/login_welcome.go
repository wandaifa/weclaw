package messaging

import (
	"context"
	"github.com/fastclaw-ai/weclaw/ilink"
)

const LoginWelcomeText = "已登录成功，微信 ClawBot 已上线。直接发消息即可开始对话。"

// Called by the serialized Bot/user queue, after the access gate.
// A failed send remains pending until the next inbound message.
func sendPendingLoginWelcome(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) error {
	if msg.ContextToken == "" {
		return nil
	}
	recipient, err := ilink.PendingLoginWelcomeUser(client.BotID())
	if err != nil {
		return err
	}
	if recipient == "" || recipient != msg.FromUserID {
		return nil
	}
	if err := SendTextReply(ctx, client, msg.FromUserID, LoginWelcomeText, msg.ContextToken, ""); err != nil {
		return err
	}
	return ilink.CompleteLoginWelcome(client.BotID())
}
