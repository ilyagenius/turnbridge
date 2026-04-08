package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net/http"
	"strings"
	"time"
)

type Profile struct {
	UserAgent       string
	SecChUa         string
	SecChUaMobile   string
	SecChUaPlatform string
}

var maleFirstNames = []string{
	"Александр", "Алексей", "Андрей", "Антон", "Арсений", "Артур", "Артём",
	"Богдан", "Валерий", "Василий", "Виктор", "Владислав", "Глеб", "Григорий",
	"Даниил", "Денис", "Дмитрий", "Евгений", "Егор", "Иван", "Игорь", "Илья",
	"Кирилл", "Леонид", "Максим", "Марк", "Матвей", "Михаил", "Никита", "Николай",
	"Олег", "Павел", "Пётр", "Роман", "Руслан", "Сергей", "Станислав", "Тимофей", "Фёдор",
}

var femaleFirstNames = []string{
	"Алина", "Алёна", "Анастасия", "Ангелина", "Анна", "Вера", "Вероника",
	"Виктория", "Дарья", "Ева", "Екатерина", "Елена", "Елизавета", "Ирина",
	"Кира", "Кристина", "Ксения", "Любовь", "Маргарита", "Марина", "Мария",
	"Милана", "Надежда", "Наталья", "Ольга", "Полина", "Светлана", "София",
	"Татьяна", "Юлия", "Яна",
}

var lastNames = []string{
	"Алексеев", "Андреев", "Антонов", "Баранов", "Белов", "Белый", "Бельский",
	"Беляев", "Борисов", "Васильев", "Великий", "Волков", "Воробьёв", "Григорьев",
	"Давыдов", "Егоров", "Жуков", "Зайцев", "Захаров", "Иванов", "Калинин",
	"Ковалёв", "Козлов", "Комаров", "Крамской", "Кузнецов", "Кузьмин", "Лебедев",
	"Макаров", "Медведев", "Михайлов", "Морозов", "Никитин", "Николаев", "Новиков",
	"Орлов", "Островский", "Павлов", "Петров", "Покровский", "Попов", "Раевский",
	"Романов", "Семёнов", "Сергеев", "Смирнов", "Соколов", "Соловьёв", "Степанов",
	"Тарасов", "Титов", "Толстой", "Трубецкой", "Филиппов", "Фролов", "Фёдоров",
	"Чайковский", "Черный", "Яковлев",
}

var profiles = []Profile{
	// Windows Chrome
	{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		SecChUa:         `"Chromium";v="146", "Not-A.Brand";v="24", "Google Chrome";v="146"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
	},
	{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		SecChUa:         `"Chromium";v="145", "Not-A.Brand";v="99", "Google Chrome";v="145"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
	},
	{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		SecChUa:         `"Chromium";v="144", "Not-A.Brand";v="8", "Google Chrome";v="144"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
	},
	// Windows Edge
	{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0",
		SecChUa:         `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
	},
	{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36 Edg/145.0.0.0",
		SecChUa:         `"Chromium";v="145", "Not-A.Brand";v="99", "Microsoft Edge";v="145"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
	},
	// macOS Chrome
	{
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		SecChUa:         `"Chromium";v="146", "Not-A.Brand";v="24", "Google Chrome";v="146"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"macOS"`,
	},
	{
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		SecChUa:         `"Chromium";v="145", "Not-A.Brand";v="99", "Google Chrome";v="145"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"macOS"`,
	},
	// Linux Chrome
	{
		UserAgent:       "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		SecChUa:         `"Chromium";v="146", "Not-A.Brand";v="24", "Google Chrome";v="146"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Linux"`,
	},
	{
		UserAgent:       "Mozilla/5.0 (X11; Ubuntu; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		SecChUa:         `"Chromium";v="144", "Not-A.Brand";v="8", "Google Chrome";v="144"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Linux"`,
	},
}

func getRandomProfile() Profile {
	return profiles[mathrand.Intn(len(profiles))]
}

func convertToFemaleSurname(surname string) string {
	if strings.HasSuffix(surname, "ий") || strings.HasSuffix(surname, "ый") || strings.HasSuffix(surname, "ой") {
		return surname[:len(surname)-4] + "ая"
	}
	if strings.HasSuffix(surname, "ов") || strings.HasSuffix(surname, "ев") ||
		strings.HasSuffix(surname, "ин") || strings.HasSuffix(surname, "ын") ||
		strings.HasSuffix(surname, "ёв") {
		return surname + "а"
	}
	return surname
}

func generateName() string {
	isFemale := mathrand.Intn(2) == 0

	var fn string
	if isFemale {
		fn = femaleFirstNames[mathrand.Intn(len(femaleFirstNames))]
	} else {
		fn = maleFirstNames[mathrand.Intn(len(maleFirstNames))]
	}

	if mathrand.Float32() < 0.3 {
		return fn
	}

	ln := lastNames[mathrand.Intn(len(lastNames))]
	if isFemale {
		ln = convertToFemaleSurname(ln)
	}

	return fmt.Sprintf("%s %s", fn, ln)
}

func generateBrowserFp(p Profile) string {
	data := p.UserAgent + p.SecChUa + "1920x1080x24"
	h := md5.Sum([]byte(data))
	return hex.EncodeToString(h[:])
}

func buildCaptchaDeviceJSON(p Profile) string {
	return fmt.Sprintf(
		`{"screenWidth":1920,"screenHeight":1080,"screenAvailWidth":1920,"screenAvailHeight":1040,"innerWidth":1920,"innerHeight":969,"devicePixelRatio":1,"language":"en-US","languages":["en-US"],"webdriver":false,"hardwareConcurrency":8,"deviceMemory":8,"connectionEffectiveType":"4g","notificationsPermission":"default","userAgent":"%s","platform":"Win32"}`,
		p.UserAgent,
	)
}

func generateFakeCursor() string {
	startX := 600 + mathrand.Intn(400)
	startY := 300 + mathrand.Intn(200)
	startTime := time.Now().UnixMilli() - int64(mathrand.Intn(2000)+1000)
	var points []string
	for i := 0; i < 15+mathrand.Intn(10); i++ {
		startX += mathrand.Intn(15) - 5
		startY += mathrand.Intn(15) + 2
		startTime += int64(mathrand.Intn(40) + 10)
		points = append(points, fmt.Sprintf(`{"x":%d,"y":%d,"t":%d}`, startX, startY, startTime))
	}
	return "[" + strings.Join(points, ",") + "]"
}

func generateSliderCursor(candidateIndex int, candidateCount int) string {
	return buildSliderCursor(candidateIndex, candidateCount, time.Now().Add(-220*time.Millisecond).UnixMilli())
}

func buildSliderCursor(candidateIndex int, candidateCount int, startTime int64) string {
	if candidateCount <= 0 {
		return "[]"
	}

	type cursorPoint struct {
		X int   `json:"x"`
		Y int   `json:"y"`
		T int64 `json:"t"`
	}

	startX := 140
	endX := startX + 620*candidateIndex/candidateCount
	startY := 430

	points := make([]cursorPoint, 0, 12)
	for step := 0; step < 12; step++ {
		x := startX + (endX-startX)*step/11
		y := startY + ((step % 3) - 1)
		points = append(points, cursorPoint{
			X: x,
			Y: y,
			T: startTime + int64(step*18),
		})
	}

	data, err := json.Marshal(points)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func applyBrowserHeaders(req *http.Request, p Profile) {
	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("sec-ch-ua", p.SecChUa)
	req.Header.Set("sec-ch-ua-mobile", p.SecChUaMobile)
	req.Header.Set("sec-ch-ua-platform", p.SecChUaPlatform)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("DNT", "1")
}
