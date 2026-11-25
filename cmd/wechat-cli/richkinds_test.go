package main

import (
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
	if wxkind.Resolve(49, 4) != "link" || wxkind.Resolve(49, 17) != "location_share" {
		t.Fatal("new app subtypes not named")
	}
}
