package cli

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/slack-go/slack"
)

func pngOf(w, h int, c color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// TestHalfBlock: 한 칸 = 위(전경)·아래(배경) 두 픽셀, 투명은 칠하지 않음, 넓으면 비율 유지해 줄임, 줄일 때 평균.
func TestHalfBlock(t *testing.T) {
	trueColor(t)
	red, blue, clear := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}, color.NRGBA{}
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, red)
	img.Set(1, 0, red)
	img.Set(0, 1, blue)
	img.Set(1, 1, blue)
	l := halfBlock(img, 2, 2)
	if len(l) != 1 || ansi.StringWidth(l[0]) != 2 || strings.Count(l[0], "▀") != 2 ||
		!strings.Contains(l[0], "38;2;255;0;0") || !strings.Contains(l[0], "48;2;0;0;255") {
		t.Errorf("빨강 위 파랑 아래: %q", l)
	}
	img.Set(0, 1, clear) // 아래 투명 → ▀ 전경만
	img.Set(1, 0, clear) // 위 투명 → ▄ 전경 = 아래
	if l := halfBlock(img, 2, 2); !strings.Contains(l[0], "▀") || !strings.Contains(l[0], "▄") || strings.Count(l[0], "48;2") != 0 {
		t.Errorf("투명: %q", l)
	}
	if w, h := fit(80, 20, 40, 24); w != 40 || h != 10 {
		t.Errorf("80×20 → 40×10: %d×%d", w, h)
	}
	if l := halfBlock(img, 4, 6); len(l) != 3 || ansi.StringWidth(l[0]) != 4 { // 키워도 정확히 그 크기
		t.Errorf("2×2 → 4칸×3줄: %d줄 %d칸", len(l), ansi.StringWidth(l[0]))
	}
	two := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	two.Set(0, 0, red)
	two.Set(1, 0, blue)
	if c := color.NRGBAModel.Convert(shrink(two, 1, 1).At(0, 0)).(color.NRGBA); c.R < 120 || c.R > 135 || c.B < 120 || c.B > 135 || c.A != 255 {
		t.Errorf("평균: %v", c)
	}
}

// TestImageFetch: 썸네일만 토큰, 첨부·블록 그림은 토큰·쿠키 없이 https로만. 너무 크거나 깨지면 실패.
func TestImageFetch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s, err := (&app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}).open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var hdr http.Header
	ext := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hdr = r.Header.Clone()
		mu.Unlock()
		switch r.URL.Path {
		case "/big":
			w.Write(make([]byte, imgMaxBytes+1))
		case "/huge": // 2049² > 4M px
			w.Write(pngOf(2049, 2049, color.Black))
		case "/junk":
			w.Write([]byte("not an image"))
		default:
			w.Write(pngOf(4, 4, color.White))
		}
	}))
	t.Cleanup(ext.Close)
	old := imageHTTP
	imageHTTP = ext.Client() // 127.0.0.1 — publicOnly는 TestPublicOnly에서
	t.Cleanup(func() { imageHTTP = old })

	if img, err := fetchImage(ctx, s, imgURL{apiURL + "img/a", true}); err != nil || img == nil || len(f.imgAuth) != 1 || f.imgAuth[0] != "Bearer xoxp-test" {
		t.Errorf("썸네일: %v %v", err, f.imgAuth)
	}
	if img, err := fetchImage(ctx, s, imgURL{ext.URL + "/ok", false}); err != nil || img == nil || hdr.Get("Authorization") != "" || hdr.Get("Cookie") != "" {
		t.Errorf("외부: %v %v", err, hdr)
	}
	if _, err := fetchImage(ctx, s, imgURL{apiURL + "img/b", false}); err == nil || len(f.imgAuth) != 1 { // 첨부 그림(auth=false)은 Slack 서버여도 받는 클라이언트가 외부용 — http라 거절, 토큰 요청 없음
		t.Errorf("첨부의 Slack URL: %v %v", err, f.imgAuth)
	}
	if _, err := fetchImage(ctx, s, imgURL{ext.URL + "/ok", true}); err != nil || hdr.Get("Authorization") != "" { // Slack 파일 호스트가 아니면 썸네일이어도 토큰 없이
		t.Errorf("썸네일 외부 호스트: %v %v", err, hdr)
	}
	for _, p := range []string{"/big", "/huge", "/junk"} {
		if img, err := fetchImage(ctx, s, imgURL{ext.URL + p, false}); err == nil || img != nil {
			t.Errorf("%s: 실패해야 한다", p)
		}
	}
}

func TestPublicOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:443": false, "10.0.0.1:443": false, "192.168.0.1:443": false, "169.254.169.254:80": false,
		"[::1]:443": false, "[::ffff:127.0.0.1]:443": false, "0.0.0.0:443": false, "100.100.1.1:443": false, "[fd00::1]:443": false,
		"255.255.255.255:443": false, "[64:ff9b::a00:1]:443": false, "[2002:a00:1::1]:443": false,
		"8.8.8.8:443": true, "[2606:4700::1111]:443": true,
	} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

// TestTUIPreviewImages: 그림 URL마다 Cmd 하나(같은 URL은 한 번), 크기 아는 그림은 와도 줄 수가 같고 고른 메시지 그대로,
// 불러오는 중 화면은 안 덮고, off면 받지 않는다.
func TestTUIPreviewImages(t *testing.T) {
	_, m := filledTUI(t)
	msgs := []slack.Message{
		{Msg: slack.Msg{Timestamp: "1700000001.000000", User: "U1AAAAAAAA", Text: "사진",
			Files: []slack.File{{ID: "F1", Name: "a.png", Thumb360: apiURL + "img/a", Thumb360W: 360, Thumb360H: 240}}}},
		{Msg: slack.Msg{Timestamp: "1700000002.000000", User: "U1AAAAAAAA", Text: "다시",
			Files:       []slack.File{{ID: "F1", Name: "a.png", Thumb360: apiURL + "img/a", Thumb360W: 360, Thumb360H: 240}},
			Attachments: []slack.Attachment{{Title: "unfurl", ImageURL: "https://example.invalid/x.png"}}}}, // 받지 않는다(Cmd만 센다)
	}
	m.Update(press("enter"))
	_, cmd := m.Update(previewMsg{seq: m.seq, head: "#general", ch: "C1GENERAL0", msgs: msgs})
	batch, _ := cmd().(tea.BatchMsg)
	if len(batch) != 2 {
		t.Fatalf("URL마다 하나, 같은 URL은 한 번: %d", len(batch))
	}
	n, sel := len(m.pvLines), m.sel
	if !strings.Contains(strings.Join(m.pvLines, "\n"), "[image]") {
		t.Errorf("받는 중 자리: %q", m.pvLines)
	}
	im := batch[0]().(imageMsg)
	if im.img == nil {
		t.Fatal("썸네일을 못 받음")
	}
	m.Update(im)
	if j := strings.Join(m.pvLines, "\n"); len(m.pvLines) != n || m.sel != sel || strings.Count(j, "a.png") != 2 || !strings.Contains(j, "▀") {
		t.Errorf("그림이 와도 줄 수(%d→%d)·고른 것 그대로: %q", n, len(m.pvLines), m.pvLines)
	}
	if _, cmd := m.Update(previewMsg{seq: m.seq, head: "#general", ch: "C1GENERAL0", msgs: msgs}); cmd != nil {
		if b, _ := cmd().(tea.BatchMsg); len(b) != 0 {
			t.Errorf("캐시에 있으면 다시 안 받음: %d", len(b))
		}
	}
	m.pvMsgs, m.pvLines = nil, []string{"#general", "불러오는 중…"}
	if m.Update(imageMsg{url: apiURL + "img/a"}); len(m.pvLines) != 2 {
		t.Errorf("불러오는 중 화면을 덮음: %q", m.pvLines)
	}

	t.Setenv("GSLACK_IMAGES", "off")
	_, m = filledTUI(t)
	m.Update(press("enter"))
	if _, cmd := m.Update(previewMsg{seq: m.seq, head: "#general", ch: "C1GENERAL0", msgs: msgs}); cmd != nil || !strings.Contains(strings.Join(m.pvLines, "\n"), "[file: a.png]") {
		t.Errorf("off: %v %q", cmd, m.pvLines)
	}
}

// TestPictureInAttachment: 첨부 막대 안 그림은 줄마다 막대가 붙고 좁은 폭에서도 접히지 않는다. 실패해도 크기를 알면 자리 그대로.
func TestPictureInAttachment(t *testing.T) {
	s := renderSession(t)
	ctx := context.Background()
	raw := slack.Message{Msg: slack.Msg{Timestamp: "1700000001.000000", User: "U1AAAAAAAA",
		Attachments: []slack.Attachment{{Title: "chart", ImageURL: "https://img/x.png", ImageWidth: 300, ImageHeight: 200, Footer: "Grafana"}}}}
	cm := s.message(ctx, raw, "C1GENERAL0")
	imgs := map[string]pvImage{"https://img/x.png": {img: shrink(image.NewNRGBA(image.Rect(0, 0, 300, 200)), imgCols, imgRows*2), done: true}}
	lines := s.tuiLines(ctx, raw, cm, 30, imgs)
	pics := 0
	for _, l := range lines[1:] {
		if !strings.Contains(l, "▌") || ansi.StringWidth(l) > 30 {
			t.Errorf("막대·폭: %q", l)
		}
		if strings.Contains(l, "▀") || strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ansi.Strip(l)), "▌")) == "" {
			pics++
		}
	}
	if w, h := fit(300, 200, 30-len(bodyIndent)-2, imgRows*2); pics != (h+1)/2 || w != 21 {
		t.Errorf("그림 줄 %d, 상자 %d×%d", pics, w, h)
	}
	imgs["https://img/x.png"] = pvImage{done: true} // 실패
	if failed := s.tuiLines(ctx, raw, cm, 30, imgs); len(failed) != len(lines) {
		t.Errorf("실패해도 자리 그대로: %d → %d", len(lines), len(failed))
	}
}
