package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // 첫 프레임만 읽는다
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/slack-go/slack"
)

// 미리보기 그림(T7) — 반블록 ▀ 한 칸 = 위아래 두 픽셀(픽셀이 정사각형). 받기는 Cmd, 캐시는 모델(m.imgs).
// 토큰은 올린 파일의 썸네일(files.slack.com)에만. 첨부·블록 그림은 보낸 사람이 고른 URL이라 토큰 없이, 공개 IP로만 받는다.

const (
	imgCols, imgRows = 40, 12 // 최대 상자(칸) = 40×24px
	imgMaxBytes      = 10 << 20
	imgMaxPixels     = 4 << 20 // 디코드 메모리 ≤ 16MB(압축 폭탄)
)

type (
	pvImage struct {
		img  image.Image // 최대 상자로 줄인 것. 실패면 nil
		done bool        // 아니면 받는 중
	}
	imageMsg struct {
		url string
		img image.Image // 실패면 nil
	}
	imgURL struct {
		url  string
		auth bool // 올린 파일 썸네일 — Slack 파일 호스트면 토큰으로
	}
)

// imageURLs는 메시지에서 그릴 그림 URL이다(올린 파일 썸네일, 첨부 image_url, image 블록).
func imageURLs(raw slack.Message) []imgURL {
	var out []imgURL
	for _, f := range raw.Files {
		if f.Thumb360 != "" {
			out = append(out, imgURL{f.Thumb360, true})
		}
	}
	blocks := func(bs []slack.Block) {
		for _, b := range bs {
			if b, ok := b.(*slack.ImageBlock); ok && b.ImageURL != "" {
				out = append(out, imgURL{b.ImageURL, false})
			}
		}
	}
	blocks(raw.Blocks.BlockSet)
	for _, a := range raw.Attachments {
		if a.ImageURL != "" {
			out = append(out, imgURL{a.ImageURL, false})
		}
		blocks(a.Blocks.BlockSet)
	}
	return out
}

// fetchImages는 아직 안 받은 그림마다 Cmd 하나. 캐시가 없으면(GSLACK_IMAGES=off) 아무것도 안 한다.
func (m *tuiModel) fetchImages(raws []slack.Message) tea.Cmd {
	if m.imgs == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, raw := range raws {
		for _, u := range imageURLs(raw) {
			if _, ok := m.imgs[u.url]; ok {
				continue
			}
			m.imgs[u.url] = pvImage{}
			ctx, s := m.ctx, m.s
			cmds = append(cmds, func() tea.Msg {
				img, _ := fetchImage(ctx, s, u) // 실패는 글자 자리표시로 남는다
				return imageMsg{u.url, img}
			})
		}
	}
	return tea.Batch(cmds...)
}

// imgSem은 동시에 받는 그림 수다 — 그림 많은 채널에서 받기 버퍼·디코드 메모리가 한꺼번에 잡히지 않게.
var imgSem = make(chan struct{}, 4)

func fetchImage(ctx context.Context, s *session, u imgURL) (image.Image, error) {
	select {
	case imgSem <- struct{}{}:
		defer func() { <-imgSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var buf bytes.Buffer
	var err error
	if p, perr := url.Parse(u.url); u.auth && perr == nil && (p.Scheme == "https" && p.Host == "files.slack.com" || apiURL != "" && strings.HasPrefix(u.url, apiURL)) {
		err = s.files.GetFileContext(ctx, u.url, &limitWriter{&buf, imgMaxBytes})
	} else {
		err = getExternal(ctx, u.url, &buf)
	}
	if err != nil {
		return nil, err
	}
	return decodeImage(buf.Bytes())
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n -= len(p); l.n < 0 {
		return 0, errors.New("그림이 너무 크다")
	}
	return l.w.Write(p)
}

// imageHTTP는 토큰·쿠키 없는 외부 그림용이다. 프록시를 안 써야 publicOnly가 실제 상대 주소를 본다.
var imageHTTP = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("리다이렉트가 너무 많다")
		}
		if req.URL.Scheme != "https" {
			return errors.New("https만")
		}
		return nil
	},
}

// 공개 주소처럼 보여도 안쪽을 가리킬 수 있는 대역 — CGNAT(Tailscale 등), NAT64·6to4(IPv4를 품는다).
var notPublic = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("2002::/16")}

// publicOnly는 DNS를 푼 뒤 실제 연결 주소를 본다 — 보낸 사람이 고른 URL로 내 LAN·localhost를 찌르지 못하게(리다이렉트·DNS 리바인딩 포함).
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || slices.ContainsFunc(notPublic, func(p netip.Prefix) bool { return p.Contains(ip) }) {
		return fmt.Errorf("공개 주소가 아니다: %s", ip) // IsGlobalUnicast가 루프백·링크로컬·멀티캐스트·미지정·브로드캐스트를 거른다
	}
	return nil
}

func getExternal(ctx context.Context, u string, w io.Writer) error {
	if !strings.HasPrefix(strings.ToLower(u), "https://") {
		return errors.New("https만")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := imageHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	_, err = io.Copy(&limitWriter{w, imgMaxBytes}, resp.Body)
	return err
}

// decodeImage는 크기를 먼저 보고(압축 폭탄) 디코드한 뒤 최대 상자로 줄인다 — 원본은 들고 있지 않는다.
func decodeImage(b []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > imgMaxPixels/cfg.Height {
		return nil, fmt.Errorf("그림이 너무 크다: %d×%d", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return shrink(img, imgCols, imgRows*2), nil
}

// fit은 비율을 지켜 w×h를 maxW×maxH 안에 넣는다. 키우지 않는다.
func fit(w, h, maxW, maxH int) (int, int) {
	if w > maxW {
		w, h = maxW, max(h*maxW/w, 1)
	}
	if h > maxH {
		w, h = max(w*maxH/h, 1), maxH
	}
	return w, h
}

// shrink는 비율을 지켜 maxW×maxH 안으로 줄인다.
func shrink(src image.Image, maxW, maxH int) image.Image {
	w, h := fit(src.Bounds().Dx(), src.Bounds().Dy(), maxW, maxH)
	return resize(src, w, h)
}

// resize는 정확히 w×h로 — 픽셀마다 원본 상자의 평균(box filter). 키우면 가장 가까운 픽셀.
func resize(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	if w == b.Dx() && h == b.Dy() {
		return src
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0 := b.Min.Y + y*b.Dy()/h
		y1 := max(b.Min.Y+(y+1)*b.Dy()/h, y0+1)
		for x := range w {
			x0 := b.Min.X + x*b.Dx()/w
			x1 := max(b.Min.X+(x+1)*b.Dx()/w, x0+1)
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA() // 알파를 곱한 값이라 투명 픽셀이 색을 끌지 않는다
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			dst.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), uint16(a / n)})
		}
	}
	return dst
}

// halfBlock은 그림을 w칸 × h픽셀(h/2줄)로 칠한다. 투명(알파 < 128)은 칠하지 않는다 — 테마 배경이 비친다.
func halfBlock(img image.Image, w, h int) []string {
	img = resize(img, w, h)
	b := img.Bounds()
	var lines []string
	for y := b.Min.Y; y < b.Max.Y; y += 2 {
		var sb strings.Builder
		for x := b.Min.X; x < b.Max.X; x++ {
			top, bot := hexColor(img.At(x, y)), ""
			if y+1 < b.Max.Y {
				bot = hexColor(img.At(x, y+1))
			}
			st, ch := lipgloss.NewStyle(), "▀"
			switch {
			case top == "" && bot == "":
				ch = " "
			case top == "":
				st, ch = st.Foreground(lipgloss.Color(bot)), "▄"
			case bot == "":
				st = st.Foreground(lipgloss.Color(top))
			default:
				st = st.Foreground(lipgloss.Color(top)).Background(lipgloss.Color(bot))
			}
			sb.WriteString(st.Render(ch))
		}
		lines = append(lines, sb.String())
	}
	return lines
}

func hexColor(c color.Color) string {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	if n.A < 128 {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", n.R, n.G, n.B)
}

// picture는 그림 자리 span이다(raw — 접두사는 prefixed가 붙인다).
// 크기(w·h)를 알면 받기 전부터 그만큼 줄을 잡고 그림도 그 크기로 그린다 — 그림이 와도 줄 수가 안 바뀐다.
// 실패해도 크기를 알면 자리 그대로. 안 받았거나(off), 크기 모르는 그림이 실패하면 nil — 부르는 쪽이 글자 자리표시를 쓴다.
// ponytail: 크기를 모르면(image 블록 등) 받은 뒤 줄 수가 바뀌어 위쪽 그림이면 보던 줄이 밀린다. 불편하면 고른 메시지 기준 앵커.
// ponytail: imageMsg마다 미리보기 전체를 다시 칠한다(그림 N개면 N²). 버벅이면 (url, w, h) → 줄 캐시.
func picture(imgs map[string]pvImage, u string, w, h, cols int) []span {
	p, ok := imgs[u]
	if !ok || p.done && p.img == nil && (w <= 0 || h <= 0) {
		return nil
	}
	if (w <= 0 || h <= 0) && p.img != nil {
		w, h = p.img.Bounds().Dx(), p.img.Bounds().Dy()
	}
	lines := []string{dimStyle.Render("[image]")}
	if w > 0 && h > 0 {
		w, h = fit(w, h, cols, imgRows*2)
		if p.img != nil {
			lines = halfBlock(p.img, w, h)
		} else {
			lines = append(lines, slices.Repeat([]string{" "}, (h+1)/2-1)...) // ""가 아니라 " " — 끝의 빈 줄은 splitLines가 버린다
		}
	}
	return []span{{text: strings.Join(lines, "\n") + "\n", raw: true}}
}
