package main

import (
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/r266-tech/wx-cli/v2/internal/wcdb"
	"github.com/r266-tech/wx-cli/v2/internal/wxkind"
)

func richRow(localType int64, content string) wcdb.Row {
	return enrichMessages([]wcdb.Row{{"local_type": localType, "message_content": content}})[0]
}

func TestRichKindsRenderWhatWeChatShows(t *testing.T) {
	cases := []struct {
		name      string
		localType int64
		content   string
		kind      string
		text      string
		payload   string
	}{
		{"mail", 35, `<msg><pushmail><content><subject><![CDATA[周报]]></subject><attach>1</attach><sender><![CDATA[小明]]></sender><digest><![CDATA[本周进展如下]]></digest><date>2026-11-24</date><fromlist count="1"><item><name><![CDATA[小明]]></name><addr>xm@example.com</addr></item></fromlist><tolist count="1"><item><name><![CDATA[我]]></name><addr>me@example.com</addr></item></tolist></content><mailid>1</mailid></pushmail></msg>`,
			"mail", "[邮件] 周报 — 小明：本周进展如下", "mail"},
		{"call", 50, `<voipmsg type="VoIPBubbleMsg"><VoIPBubbleMsg><msg><![CDATA[通话时长 00:12]]></msg><room_type>1</room_type><duration>12</duration></VoIPBubbleMsg></voipmsg>`,
			"voip", "[通话] 通话时长 00:12", "call"},
		{"channel video", packedLocalType(49, 51), `<msg><appmsg appid="" sdkver="0"><title>当前微信版本不支持展示该内容，请升级至最新版本。</title><type>51</type><url>https://support.weixin.qq.com/update/</url><finderFeed><nickname><![CDATA[作者甲]]></nickname><desc><![CDATA[一段描述]]></desc><mediaList><media><coverUrl><![CDATA[http://finder.video.qq.com/c.jpg]]></coverUrl><videoPlayDuration><![CDATA[42]]></videoPlayDuration></media></mediaList></finderFeed></appmsg></msg>`,
			"channel_video", "[视频号] 作者甲：一段描述", "channel_video"},
		{"channel live", packedLocalType(49, 63), `<msg><appmsg appid="" sdkver="0"><title>x</title><type>63</type><finderLive><nickname>主播乙</nickname><desc>今晚聊聊</desc></finderLive></appmsg></msg>`,
			"channel_live", "[视频号直播] 主播乙：今晚聊聊", "channel_live"},
		{"announcement", packedLocalType(49, 87), `<msg><appmsg appid="" sdkver="0"><type>87</type><announcement><![CDATA[<group_notice_item/>]]></announcement><textannouncement><![CDATA[周五停电]]></textannouncement></appmsg></msg>`,
			"announcement", "[群公告] 周五停电", "announcement"},
		{"gift", packedLocalType(49, 124), `<msg><appmsg appid="" sdkver="0"><title>微信礼物</title><type>124</type><ecsgift><wishmessage><![CDATA[生日快乐]]></wishmessage><gifttitle><![CDATA[礼物]]></gifttitle><gifts><gift><skutitle><![CDATA[一束花]]></skutitle></gift></gifts></ecsgift></appmsg></msg>`,
			"gift", "[微信礼物] 一束花：生日快乐", "gift"},
		{"channel card", packedLocalType(49, 50), `<msg><appmsg appid="" sdkver="0"><title /><type>50</type><findernamecard><username>v2_x@finder</username><nickname>手作丁</nickname></findernamecard></appmsg></msg>`,
			"channel_card", "[视频号名片] 手作丁", "channel_card"},
		{"channel topic", packedLocalType(49, 75), `<msg><appmsg appid="" sdkver="0"><title>当前微信版本不支持展示该内容，请升级至最新版本。</title><type>75</type><finder><type>4</type><detail><finderTopic><topic><![CDATA[某首歌]]></topic><desc><![CDATA[9.6万次使用]]></desc><event><title><![CDATA[视频号 · 音乐]]></title></event></finderTopic></detail></finder></appmsg></msg>`,
			"channel_topic", "[视频号 · 音乐] 某首歌（9.6万次使用）", "channel_topic"},
		{"openim card", 66, `<?xml version="1.0"?><msg username="x@openim" nickname="企业联系人丙" />`,
			"card", "[名片] 企业联系人丙", "card"},
	}
	for _, c := range cases {
		row := richRow(c.localType, c.content)
		if row["kind_name"] != c.kind {
			t.Errorf("%s: kind %v, want %s", c.name, row["kind_name"], c.kind)
		}
		msg := agentMessage(row)
		if msg["text"] != c.text {
			t.Errorf("%s: text %q, want %q", c.name, msg["text"], c.text)
		}
		if msg[c.payload] == nil {
			t.Errorf("%s: missing %s payload: %#v", c.name, c.payload, msg)
		}
		if c.kind == "channel_video" && msg["link"] != nil {
			t.Errorf("%s: placeholder link kept", c.name)
		}
	}
	if wxkind.Resolve(49, 8) != "sticker" || wxkind.Resolve(49, 50) != "channel_card" {
		t.Fatal("attachment sticker / channel card not named")
	}
	if wxkind.Resolve(49, 4) != "link" || wxkind.Resolve(49, 17) != "location_share" {
		t.Fatal("new app subtypes not named")
	}
}

func TestAttachmentStickerFeedsStickerPipeline(t *testing.T) {
	sum := "cba76b9ab3830dfeb9bd841eddd6c9f9"
	want := "http://wxapp.tc.qq.com/262/20304/stodownload?m=" + sum + "&filekey=1"
	other := "http://wxapp.tc.qq.com/262/20304/stodownload?m=b9e3b1a5a75adaa4a06a1ed98b37440b&filekey=2"
	field := func(num byte, v string) []byte {
		b := []byte{num<<3 | 2}
		b = binary.AppendUvarint(b, uint64(len(v)))
		return append(b, v...)
	}
	pb := append(append(append(field(1, sum), field(2, want)...), field(5, other)...), 0x30, 0x01)
	content := `<msg><appmsg appid="" sdkver="0"><title /><type>8</type><appattach><totallen>1641978</totallen><emoticonmd5>` + sum +
		`</emoticonmd5><fileext>pic</fileext><cdnthumbwidth>300</cdnthumbwidth><cdnthumbheight>263</cdnthumbheight><emojiinfo>` +
		base64.StdEncoding.EncodeToString(pb) + `</emojiinfo></appattach></appmsg></msg>`
	row := richRow(packedLocalType(49, 8), content)
	if row["kind_name"] != "sticker" {
		t.Fatalf("kind = %v", row["kind_name"])
	}
	p, _ := row["message_content_parsed"].(map[string]any)
	if stringMapValue(p, "md5") != sum || stringMapValue(p, "cdn_url") != want {
		t.Fatalf("parsed = %#v", p)
	}
	if msg := agentMessage(row); msg["text"] != "[表情]" {
		t.Fatalf("text = %v", msg["text"])
	}
	if emojiInfoURL("!!", sum) != "" {
		t.Fatal("bad base64 must yield no URL")
	}
}

func TestTransferAndRedPacketLines(t *testing.T) {
	transfer := func(sub, memo string) string {
		return `<msg><appmsg appid="" sdkver=""><title>微信转账</title><des>收到转账25.70元。如需收钱，请点此升级至最新版本</des><type>2000</type><wcpayinfo><paysubtype>` + sub +
			`</paysubtype><feedesc>￥25.70</feedesc><pay_memo>` + memo + `</pay_memo></wcpayinfo></appmsg></msg>`
	}
	for _, c := range []struct{ sub, memo, text, status string }{
		{"1", "饭钱", "[转账] ￥25.70：饭钱", "sent"},
		{"3", "", "[已收款] ￥25.70", "accepted"},
		{"4", "", "[已退还] ￥25.70", "returned"},
	} {
		msg := agentMessage(richRow(packedLocalType(49, 2000), transfer(c.sub, c.memo)))
		tr := mapAny(msg["transfer"])
		if msg["text"] != c.text || tr["status"] != c.status || tr["description"] != nil {
			t.Errorf("paysubtype %s: text %q transfer %v", c.sub, msg["text"], tr)
		}
	}
	red := `<msg><appmsg appid="" sdkver=""><title>恭喜发财，大吉大利</title><type>2001</type><wcpayinfo><sendertitle>恭喜发财，大吉大利</sendertitle><scenetext>微信红包</scenetext></wcpayinfo></appmsg></msg>`
	if msg := agentMessage(richRow(packedLocalType(49, 2001), red)); msg["text"] != "[红包] 恭喜发财，大吉大利" {
		t.Errorf("red packet text %q", msg["text"])
	}
}
