package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ====== 数据结构 ======

type HuangItem struct {
	Name      string
	BuyPrice  float64
	SellPrice float64
	High      float64
	Low       float64
}

type ZhoutuoItem struct {
	Name   string  `json:"name"`
	Symbol string  `json:"symbol"`
	BP     float64 `json:"bp"`
	SP     float64 `json:"sp"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
}

type ZhoutuoResponse struct {
	Mod1 []ZhoutuoItem `json:"mod1"`
	Mod2 []ZhoutuoItem `json:"mod2"`
	Mod3 []ZhoutuoItem `json:"mod3"`
	Mod4 []ZhoutuoItem `json:"mod4"`
}

type MetalOffset struct {
	BPOffset float64 `json:"bp_offset"`
	SPOffset float64 `json:"sp_offset"`
}

type MinuteSnapshot struct {
	Minute  string             `json:"minute"`
	RefBuy  map[string]float64 `json:"ref_buy"`
	RefSell map[string]float64 `json:"ref_sell"`
	ZtBP    map[string]float64 `json:"zt_bp"`
	ZtSP    map[string]float64 `json:"zt_sp"`
}

type CalibData struct {
	UpdatedAt string                  `json:"updated_at"`
	Offsets   map[string]MetalOffset  `json:"offsets"`
	History   []MinuteSnapshot        `json:"history"`
	// 分钟内缓存，避免同一分钟重复请求
	LastRefMinute string             `json:"last_ref_minute"`
	LastRefBuy    map[string]float64 `json:"last_ref_buy"`
	LastRefSell   map[string]float64 `json:"last_ref_sell"`
}

type MetalDef struct {
	Name    string
	HJIndex int
}

var metalDefs = []MetalDef{
	{Name: "黄金", HJIndex: 12},
	{Name: "白银", HJIndex: 16},
	{Name: "铂金", HJIndex: 20},
	{Name: "钯金", HJIndex: 24},
}

const ADJUST_FILE = "adjust.json"
const MAX_HISTORY = 10
const STABLE_MINUTES = 3
const CHANGE_THRESHOLD = 0.6

// ====== 网络请求 ======

func fetchPlain(url string) (string, error) {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("HTTP GET 失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}
	return string(body), nil
}

// ====== 黄金价格网数据源 (支持分钟内缓存) ======

func fetchHuangjinjiage(calib *CalibData) (map[string]HuangItem, bool, error) {
	minLabel := currentMinute()

	// 分钟内缓存命中：同一分钟不重复请求网络
	if calib.LastRefMinute == minLabel && len(calib.LastRefBuy) > 0 {
		result := make(map[string]HuangItem)
		for _, m := range metalDefs {
			if buy, ok := calib.LastRefBuy[m.Name]; ok {
				sell := calib.LastRefSell[m.Name]
				result[m.Name] = HuangItem{Name: m.Name, BuyPrice: buy, SellPrice: sell}
			}
		}
		return result, true, nil
	}

	// 缓存未命中，从网络获取
	minute := time.Now().Unix() / 60
	url := fmt.Sprintf("http://res.huangjinjiage.com.cn/panjia2.js?t=%d", minute)
	body, err := fetchPlain(url)
	if err != nil {
		return nil, false, err
	}

	re := regexp.MustCompile(`"\[(.*?)\]"`)
	match := re.FindStringSubmatch(body)
	if len(match) < 2 {
		re2 := regexp.MustCompile(`= \[(.*?)\];`)
		match2 := re2.FindStringSubmatch(body)
		if len(match2) < 2 {
			return nil, false, fmt.Errorf("无法解析 panjia2.js 数据")
		}
		match = match2
	}

	parts := strings.Split("["+match[1]+"]", ",")
	if len(parts) < 30 {
		return nil, false, fmt.Errorf("数据字段不足 (got %d)", len(parts))
	}

	// 更新缓存
	result := make(map[string]HuangItem)
	calib.LastRefMinute = minLabel
	calib.LastRefBuy = make(map[string]float64)
	calib.LastRefSell = make(map[string]float64)

	for _, m := range metalDefs {
		idx := m.HJIndex
		buy, _ := strconv.ParseFloat(strings.TrimSpace(parts[idx]), 64)
		sell, _ := strconv.ParseFloat(strings.TrimSpace(parts[idx+1]), 64)
		high, _ := strconv.ParseFloat(strings.TrimSpace(parts[idx+2]), 64)
		low, _ := strconv.ParseFloat(strings.TrimSpace(parts[idx+3]), 64)
		result[m.Name] = HuangItem{
			Name: m.Name, BuyPrice: buy, SellPrice: sell, High: high, Low: low,
		}
		calib.LastRefBuy[m.Name] = buy
		calib.LastRefSell[m.Name] = sell
	}
	return result, false, nil
}

// ====== 舟托数据源 ======

func fetchZhoutuo() (map[string]ZhoutuoItem, error) {
	var resp ZhoutuoResponse
	client := http.Client{Timeout: 10 * time.Second}
	r, err := client.Get("https://dhj.zhoutuoiot.cn/?ajax=1")
	if err != nil {
		return nil, fmt.Errorf("HTTP GET 失败: %v", err)
	}
	defer r.Body.Close()
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %v", err)
	}

	all := append(resp.Mod1, resp.Mod2...)
	all = append(all, resp.Mod3...)
	all = append(all, resp.Mod4...)

	result := make(map[string]ZhoutuoItem)
	for _, item := range all {
		name := strings.ReplaceAll(item.Name, " ", "")
		if item.Symbol == "黄 金" && strings.Contains(name, "黄金") {
			result["黄金"] = item
		} else if item.Symbol == "白 银" && strings.Contains(name, "白银") {
			result["白银"] = item
		} else if item.Symbol == "铂 金" && strings.Contains(name, "铂金") {
			result["铂金"] = item
		} else if item.Symbol == "钯 金" && strings.Contains(name, "钯金") {
			result["钯金"] = item
		}
	}
	return result, nil
}

// ====== 校准文件读写 ======

func loadCalib() CalibData {
	var data CalibData
	raw, err := ioutil.ReadFile(ADJUST_FILE)
	if err != nil {
		data.Offsets = make(map[string]MetalOffset)
		data.History = []MinuteSnapshot{}
		return data
	}
	json.Unmarshal(raw, &data)
	if data.Offsets == nil {
		data.Offsets = make(map[string]MetalOffset)
	}
	if data.History == nil {
		data.History = []MinuteSnapshot{}
	}
	return data
}

func saveCalib(data CalibData) error {
	raw, _ := json.MarshalIndent(data, "", "  ")
	return ioutil.WriteFile(ADJUST_FILE, raw, 0644)
}

// ====== 核心判定逻辑 ======

func currentMinute() string {
	return time.Now().Format("15:04")
}

type TrendResult struct {
	Metal       string
	SavedBP     float64
	SavedSP     float64
	RecentBP    []float64
	RecentSP    []float64
	Deviated    bool
	Consecutive int
}

func analyzeTrend(name string, saved MetalOffset, history []MinuteSnapshot) TrendResult {
	r := TrendResult{
		Metal: name, SavedBP: saved.BPOffset, SavedSP: saved.SPOffset,
	}
	for i := len(history) - 1; i >= 0; i-- {
		snap := history[i]
		bp, hasBP := snap.ZtBP[name]
		refBuy, hasBuy := snap.RefBuy[name]
		sp, hasSP := snap.ZtSP[name]
		refSell, hasSell := snap.RefSell[name]
		if hasBP && hasBuy && hasSP && hasSell {
			r.RecentBP = append(r.RecentBP, bp-refBuy)
			r.RecentSP = append(r.RecentSP, sp-refSell)
		}
	}
	consecutive := 0
	for i := 0; i < len(r.RecentBP); i++ {
		dBP := r.RecentBP[i] - saved.BPOffset
		dSP := r.RecentSP[i] - saved.SPOffset
		if dBP > CHANGE_THRESHOLD || dBP < -CHANGE_THRESHOLD ||
			dSP > CHANGE_THRESHOLD || dSP < -CHANGE_THRESHOLD {
			consecutive++
		} else {
			break
		}
	}
	r.Consecutive = consecutive
	r.Deviated = consecutive >= STABLE_MINUTES
	return r
}

func round1(v float64) float64 {
	return float64(int(v*10)) / 10
}

// ====== 主流程 ======

func main() {
	now := time.Now()
	nowStr := now.Format("2006-01-02 15:04:05")
	minLabel := now.Format("15:04")

	// 检测参数
	isQuick := false
	isJSON := false
	for _, a := range os.Args[1:] {
		if a == "--quick" {
			isQuick = true
		}
		if a == "--json" {
			isJSON = true
		}
	}

	if isJSON {
		runJSON()
		return
	}

	if isQuick {
		runQuick(minLabel)
		return
	}

	fmt.Printf("\n═══════════════════════════════════════════════════════════════\n")
	fmt.Printf("  贵金属报价自动校准系统\n")
	fmt.Printf("  时间: %s\n", nowStr)
	fmt.Println("  模式: 完整校准 + 趋势分析")
	fmt.Println("  提示: 高频获取请用 ./calib --quick (仅显示价格，不写磁盘)")
	fmt.Printf("═══════════════════════════════════════════════════════════════\n\n")

	// ========== 1. 加载校准文件（先加载用于缓存判断）==========
	calib := loadCalib()

	// ========== 2. 获取两个数据源 ==========
	fmt.Print(">> 获取 huangjinjiage 基准数据... ")
	hjData, fromCache, err := fetchHuangjinjiage(&calib)
	if err != nil {
		fmt.Printf("失败: %v\n", err)
		return
	}
	cacheLabel := ""
	if fromCache {
		cacheLabel = " (缓存命中)"
	}
	fmt.Printf("OK%s (金=%.2f, 银=%.2f, 铂=%.2f, 钯=%.2f)\n",
		cacheLabel,
		hjData["黄金"].SellPrice, hjData["白银"].SellPrice,
		hjData["铂金"].SellPrice, hjData["钯金"].SellPrice)

	fmt.Print(">> 获取 zhoutuo 报价盘数据... ")
	ztData, err := fetchZhoutuo()
	if err != nil {
		fmt.Printf("失败: %v\n", err)
		return
	}
	fmt.Printf("OK (%d个品种)\n\n", len(ztData))

	// ========== 3. 构建分钟快照 ==========
	snap := MinuteSnapshot{
		Minute:  minLabel,
		RefBuy:  make(map[string]float64),
		RefSell: make(map[string]float64),
		ZtBP:    make(map[string]float64),
		ZtSP:    make(map[string]float64),
	}
	for _, m := range metalDefs {
		if hj, ok := hjData[m.Name]; ok {
			snap.RefBuy[m.Name] = hj.BuyPrice
			snap.RefSell[m.Name] = hj.SellPrice
		}
		if zt, ok := ztData[m.Name]; ok {
			snap.ZtBP[m.Name] = zt.BP
			snap.ZtSP[m.Name] = zt.SP
		}
	}

	// 追加到历史（同分钟覆盖，新分钟新增）
	isNewMinute := false
	found := false
	for i := len(calib.History) - 1; i >= 0; i-- {
		if calib.History[i].Minute == minLabel {
			calib.History[i] = snap
			found = true
			break
		}
	}
	if !found {
		calib.History = append(calib.History, snap)
		isNewMinute = true
	}
	if len(calib.History) > MAX_HISTORY {
		calib.History = calib.History[len(calib.History)-MAX_HISTORY:]
	}

	// ========== 4. 品种分析 ==========
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Println("  品种对照")
	fmt.Println("───────────────────────────────────────────────────────────────")

	needUpdate := false

	for _, m := range metalDefs {
		hj, hjOK := hjData[m.Name]
		zt, ztOK := ztData[m.Name]

		fmt.Printf("  [%s]\n", m.Name)
		if hjOK {
			fmt.Printf("    基准:  回购=%.2f  销售=%.2f\n", hj.BuyPrice, hj.SellPrice)
		}
		if ztOK {
			fmt.Printf("    典和佳: bp=%-8.2f  sp=%-8.2f\n", zt.BP, zt.SP)
		}

		if hjOK && ztOK {
			bpOff := zt.BP - hj.BuyPrice
			spOff := zt.SP - hj.SellPrice
			saved, hasSaved := calib.Offsets[m.Name]

			if !hasSaved {
				calib.Offsets[m.Name] = MetalOffset{BPOffset: round1(bpOff), SPOffset: round1(spOff)}
				fmt.Printf("    → 初次设定 offset: bp=%+.1f  sp=%+.1f\n", round1(bpOff), round1(spOff))
				needUpdate = true
			} else {
				trend := analyzeTrend(m.Name, saved, calib.History)
				fmt.Printf("    保存值: bp=%+.1f  sp=%+.1f\n", saved.BPOffset, saved.SPOffset)
				fmt.Printf("    趋势:   ")
				for i, off := range trend.RecentBP {
					if i >= 5 {
						fmt.Printf("...")
						break
					}
					fmt.Printf("%+.1f ", off)
				}
				fmt.Printf("(bp)\n")

				if trend.Deviated {
					fmt.Printf("    ⚠ 检测到商家调价! 连续%d分钟偏离，更新为 bp=%+.1f sp=%+.1f\n",
						trend.Consecutive, round1(bpOff), round1(spOff))
					calib.Offsets[m.Name] = MetalOffset{BPOffset: round1(bpOff), SPOffset: round1(spOff)}
					needUpdate = true
				} else if trend.Consecutive >= 1 {
					fmt.Printf("    ~ 轻微偏离(第%d分钟)，未达阈值(%d分钟)，暂不更新\n",
						trend.Consecutive, STABLE_MINUTES)
				} else {
					fmt.Printf("    ✓ offset 稳定\n")
				}
				fmt.Printf("    → 实时基准价 ≈ 典和佳bp - (%+.1f) = %.2f\n",
					saved.BPOffset, zt.BP-saved.BPOffset)
			}
		} else if ztOK {
			if saved, ok := calib.Offsets[m.Name]; ok {
				fmt.Printf("    (使用上次offset: bp=%+.1f sp=%+.1f)\n", saved.BPOffset, saved.SPOffset)
			}
		}
		fmt.Println()
	}

	// ========== 5. 条件保存 ==========
	// 只在以下情况写磁盘:
	//   a) offset 有变化 (needUpdate)
	//   b) 新增了一个分钟记录 (isNewMinute)
	// 同一分钟内多次运行且 offset 无变化 → 不写磁盘
	if needUpdate || isNewMinute {
		calib.UpdatedAt = nowStr
		if err := saveCalib(calib); err != nil {
			fmt.Printf("  ⚠ 保存失败: %v\n", err)
		} else {
			reason := "新分钟记录"
			if needUpdate {
				reason = "offset已更新"
			}
			fmt.Printf("  ✔ adjust.json 已保存 (%s)\n", reason)
		}
	} else {
		fmt.Println("  ✔ 无变化，跳过磁盘写入 (同一分钟内不重复写)")
	}

	// ========== 6. 历史趋势表 ==========
	fmt.Println()
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Println("  历史趋势表")
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Printf("  分钟     ")
	for _, m := range metalDefs {
		fmt.Printf("  %s-offset   ", m.Name)
	}
	fmt.Println()

	start := 0
	if len(calib.History) > 8 {
		start = len(calib.History) - 8
	}
	for i := start; i < len(calib.History); i++ {
		s := calib.History[i]
		fmt.Printf("  %s  ", s.Minute)
		for _, m := range metalDefs {
			bp, okBP := s.ZtBP[m.Name]
			refB, okRB := s.RefBuy[m.Name]
			if okBP && okRB {
				fmt.Printf("  bp=%+.1f     ", bp-refB)
			} else if okBP {
				fmt.Printf("  %.1f(无基准) ", bp)
			} else {
				fmt.Printf("  ---           ")
			}
		}
		fmt.Println()
	}
	fmt.Println()
	fmt.Printf("  判定: 连续%d分钟偏离阈值%.1f → 判定为商家调价\n", STABLE_MINUTES, CHANGE_THRESHOLD)
	fmt.Printf("  提示: 高频获取请用 --quick 参数\n")
	fmt.Println()
}

// ====== 快速模式：仅显示实时价格 ======

func runQuick(minLabel string) {
	// 读取已有的校准参数
	calib := loadCalib()
	if len(calib.Offsets) == 0 {
		fmt.Println("  ⚠ 未找到校准参数，请先运行完整模式 (不加 --quick)")
		return
	}

	// 只获取 zhoutuo 数据（不请求基准网站，不写磁盘）
	ztData, err := fetchZhoutuo()
	if err != nil {
		fmt.Printf("失败: %v\n", err)
		return
	}

	fmt.Printf("\n═══════════════════════════════════════════════════════════════\n")
	fmt.Printf("  实时报价 (快速模式)  %s\n", minLabel)
	fmt.Printf("  公式: 基准价 ≈ 典和佳价 - offset\n")
	fmt.Println("  提示: 此模式不校准、不写磁盘、不请求基准网站")
	fmt.Printf("═══════════════════════════════════════════════════════════════\n\n")

	fmt.Printf("  %-6s  %10s  %10s  %10s  %10s  %10s\n", "品种", "典和佳bp", "典和佳sp", "基准bp", "基准sp", "offset")
	fmt.Println("  " + strings.Repeat("─", 70))

	for _, m := range metalDefs {
		zt, ztOK := ztData[m.Name]
		if !ztOK {
			continue
		}
		off, hasOff := calib.Offsets[m.Name]
		if !hasOff {
			continue
		}
		refBP := zt.BP - off.BPOffset
		refSP := zt.SP - off.SPOffset
		fmt.Printf("  %-6s  %10.2f  %10.2f  %10.2f  %10.2f  bp=%+.1f sp=%+.1f\n",
			m.Name, zt.BP, zt.SP, refBP, refSP, off.BPOffset, off.SPOffset)
	}
	fmt.Println()
}

// ====== JSON 输出模式（作为网站数据源） ======

type PriceEntry struct {
	ZtBP     float64 `json:"zt_bp"`
	ZtSP     float64 `json:"zt_sp"`
	RefBP    float64 `json:"ref_bp"`
	RefSP    float64 `json:"ref_sp"`
	BPOffset float64 `json:"bp_offset"`
	SPOffset float64 `json:"sp_offset"`
}

type JSONOutput struct {
	Time     string                 `json:"time"`
	Minute   string                 `json:"minute"`
	CalibAt  string                 `json:"calib_updated_at"`
	Prices   map[string]PriceEntry  `json:"prices"`
	Status   string                 `json:"status"`
}

func runJSON() {
	calib := loadCalib()
	now := time.Now()
	minLabel := now.Format("15:04")

	ztData, err := fetchZhoutuo()
	if err != nil {
		json.NewEncoder(os.Stdout).Encode(JSONOutput{
			Time: now.Format("15:04:05"), Minute: minLabel, Status: "error: " + err.Error(),
		})
		return
	}

	output := JSONOutput{
		Time:    now.Format("15:04:05"),
		Minute:  minLabel,
		CalibAt: calib.UpdatedAt,
		Prices:  make(map[string]PriceEntry),
		Status:  "ok",
	}

	for _, m := range metalDefs {
		zt, ztOK := ztData[m.Name]
		if !ztOK {
			continue
		}
		off, hasOff := calib.Offsets[m.Name]
		if !hasOff {
			continue
		}
		output.Prices[m.Name] = PriceEntry{
			ZtBP: zt.BP, ZtSP: zt.SP,
			RefBP: round1(zt.BP - off.BPOffset),
			RefSP: round1(zt.SP - off.SPOffset),
			BPOffset: round1(off.BPOffset),
			SPOffset: round1(off.SPOffset),
		}
	}

	json.NewEncoder(os.Stdout).Encode(output)
}
