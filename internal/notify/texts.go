// SPDX-License-Identifier: GPL-3.0-or-later

package notify

import (
	"fmt"
	"strings"
	"time"
)

// The texts of the messages in the languages of the web (its admin area);
// a dialect of the web (bar, gsw, nds, swg) gets German, an unknown code
// English. Placeholders: {name}.
var texts = map[string]map[string]string{
	"en": {
		"resetTitle": "Ranking {ranking} reset", "resetBody": "The ranking {ranking} was reset.", "by": "Requested by: {by}",
		"hallTitle": "Hall of fame {ranking}: {from} – {to}", "hallBody": "The period {from} – {to} of {ranking} has ended. The best players:",
		"nobody": "Nobody played in this period.", "hallReset": "The ranking was reset for the new period.", "points": "{n} points",
		"updateTitle": "gameWE {component} {version} is out", "updateBody": "A new release of gameWE {component} is out: {version}.", "running": "Running: {current}",
		"testTitle": "gameWE test message", "testBody": "This test message comes from the gameWE collector. Notifications by {channel} arrive like this.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"de": {
		"resetTitle": "Ranking {ranking} zurückgesetzt", "resetBody": "Das Ranking {ranking} wurde zurückgesetzt.", "by": "Ausgelöst von: {by}",
		"hallTitle": "Ruhmeshalle {ranking}: {from} – {to}", "hallBody": "Die Periode {from} – {to} von {ranking} ist zu Ende. Die besten Spieler:",
		"nobody": "In dieser Periode hat niemand gespielt.", "hallReset": "Das Ranking wurde für die neue Periode zurückgesetzt.", "points": "{n} Punkte",
		"updateTitle": "gameWE {component} {version} ist erschienen", "updateBody": "Eine neue Version von gameWE {component} ist erschienen: {version}.", "running": "Installiert: {current}",
		"testTitle": "gameWE-Testnachricht", "testBody": "Diese Testnachricht kommt vom gameWE-Collector. So kommen Benachrichtigungen per {channel} an.",
		"web": "Web", "collector": "Collector", "email": "E-Mail",
	},
	"ru": {
		"resetTitle": "Рейтинг {ranking} сброшен", "resetBody": "Рейтинг {ranking} был сброшен.", "by": "Запросил: {by}",
		"hallTitle": "Зал славы {ranking}: {from} – {to}", "hallBody": "Период {from} – {to} рейтинга {ranking} завершён. Лучшие игроки:",
		"nobody": "В этот период никто не играл.", "hallReset": "Рейтинг сброшен для нового периода.", "points": "{n} очков",
		"updateTitle": "Вышел gameWE {component} {version}", "updateBody": "Вышла новая версия gameWE {component}: {version}.", "running": "Установлена: {current}",
		"testTitle": "Тестовое сообщение gameWE", "testBody": "Это тестовое сообщение от коллектора gameWE. Так приходят уведомления через {channel}.",
		"web": "веб", "collector": "коллектор", "email": "эл. почта",
	},
	"es": {
		"resetTitle": "Ranking {ranking} reiniciado", "resetBody": "El ranking {ranking} se ha reiniciado.", "by": "Solicitado por: {by}",
		"hallTitle": "Salón de la fama {ranking}: {from} – {to}", "hallBody": "El periodo {from} – {to} de {ranking} ha terminado. Los mejores jugadores:",
		"nobody": "Nadie jugó en este periodo.", "hallReset": "El ranking se ha reiniciado para el nuevo periodo.", "points": "{n} puntos",
		"updateTitle": "Ya está disponible gameWE {component} {version}", "updateBody": "Hay una nueva versión de gameWE {component}: {version}.", "running": "Instalada: {current}",
		"testTitle": "Mensaje de prueba de gameWE", "testBody": "Este mensaje de prueba viene del collector de gameWE. Así llegan las notificaciones por {channel}.",
		"web": "web", "collector": "collector", "email": "correo",
	},
	"fr": {
		"resetTitle": "Classement {ranking} réinitialisé", "resetBody": "Le classement {ranking} a été réinitialisé.", "by": "Demandé par : {by}",
		"hallTitle": "Panthéon {ranking} : {from} – {to}", "hallBody": "La période {from} – {to} de {ranking} est terminée. Les meilleurs joueurs :",
		"nobody": "Personne n’a joué pendant cette période.", "hallReset": "Le classement a été réinitialisé pour la nouvelle période.", "points": "{n} points",
		"updateTitle": "gameWE {component} {version} est disponible", "updateBody": "Une nouvelle version de gameWE {component} est disponible : {version}.", "running": "Installée : {current}",
		"testTitle": "Message de test gameWE", "testBody": "Ce message de test vient du collecteur gameWE. Les notifications par {channel} arrivent ainsi.",
		"web": "web", "collector": "collecteur", "email": "e-mail",
	},
	"pt": {
		"resetTitle": "Ranking {ranking} reposto", "resetBody": "O ranking {ranking} foi reposto.", "by": "Pedido por: {by}",
		"hallTitle": "Hall da fama {ranking}: {from} – {to}", "hallBody": "O período {from} – {to} de {ranking} terminou. Os melhores jogadores:",
		"nobody": "Ninguém jogou neste período.", "hallReset": "O ranking foi reposto para o novo período.", "points": "{n} pontos",
		"updateTitle": "Saiu o gameWE {component} {version}", "updateBody": "Saiu uma nova versão do gameWE {component}: {version}.", "running": "Instalada: {current}",
		"testTitle": "Mensagem de teste do gameWE", "testBody": "Esta mensagem de teste vem do collector do gameWE. As notificações por {channel} chegam assim.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"pt-br": {
		"resetTitle": "Ranking {ranking} zerado", "resetBody": "O ranking {ranking} foi zerado.", "by": "Solicitado por: {by}",
		"hallTitle": "Hall da fama {ranking}: {from} – {to}", "hallBody": "O período {from} – {to} de {ranking} terminou. Os melhores jogadores:",
		"nobody": "Ninguém jogou neste período.", "hallReset": "O ranking foi zerado para o novo período.", "points": "{n} pontos",
		"updateTitle": "Saiu o gameWE {component} {version}", "updateBody": "Saiu uma nova versão do gameWE {component}: {version}.", "running": "Instalada: {current}",
		"testTitle": "Mensagem de teste do gameWE", "testBody": "Esta mensagem de teste vem do collector do gameWE. As notificações por {channel} chegam assim.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"nl": {
		"resetTitle": "Ranglijst {ranking} gereset", "resetBody": "De ranglijst {ranking} is gereset.", "by": "Aangevraagd door: {by}",
		"hallTitle": "Eregalerij {ranking}: {from} – {to}", "hallBody": "De periode {from} – {to} van {ranking} is afgelopen. De beste spelers:",
		"nobody": "In deze periode heeft niemand gespeeld.", "hallReset": "De ranglijst is gereset voor de nieuwe periode.", "points": "{n} punten",
		"updateTitle": "gameWE {component} {version} is uit", "updateBody": "Er is een nieuwe versie van gameWE {component}: {version}.", "running": "Geïnstalleerd: {current}",
		"testTitle": "gameWE-testbericht", "testBody": "Dit testbericht komt van de gameWE-collector. Zo komen meldingen via {channel} aan.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"da": {
		"resetTitle": "Rangliste {ranking} nulstillet", "resetBody": "Ranglisten {ranking} er blevet nulstillet.", "by": "Anmodet af: {by}",
		"hallTitle": "Hall of fame {ranking}: {from} – {to}", "hallBody": "Perioden {from} – {to} for {ranking} er slut. De bedste spillere:",
		"nobody": "Ingen spillede i denne periode.", "hallReset": "Ranglisten er nulstillet til den nye periode.", "points": "{n} point",
		"updateTitle": "gameWE {component} {version} er udkommet", "updateBody": "En ny version af gameWE {component} er udkommet: {version}.", "running": "Installeret: {current}",
		"testTitle": "gameWE-testbesked", "testBody": "Denne testbesked kommer fra gameWE-collectoren. Sådan ankommer notifikationer via {channel}.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"pl": {
		"resetTitle": "Ranking {ranking} zresetowany", "resetBody": "Ranking {ranking} został zresetowany.", "by": "Zlecił: {by}",
		"hallTitle": "Galeria sław {ranking}: {from} – {to}", "hallBody": "Okres {from} – {to} rankingu {ranking} dobiegł końca. Najlepsi gracze:",
		"nobody": "W tym okresie nikt nie grał.", "hallReset": "Ranking został zresetowany na nowy okres.", "points": "{n} pkt",
		"updateTitle": "Wyszedł gameWE {component} {version}", "updateBody": "Wyszła nowa wersja gameWE {component}: {version}.", "running": "Zainstalowana: {current}",
		"testTitle": "Wiadomość testowa gameWE", "testBody": "Ta wiadomość testowa pochodzi z collectora gameWE. Tak docierają powiadomienia przez {channel}.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"sr": {
		"resetTitle": "Ранг листа {ranking} ресетована", "resetBody": "Ранг листа {ranking} је ресетована.", "by": "Затражио: {by}",
		"hallTitle": "Кућа славних {ranking}: {from} – {to}", "hallBody": "Период {from} – {to} за {ranking} је завршен. Најбољи играчи:",
		"nobody": "У овом периоду нико није играо.", "hallReset": "Ранг листа је ресетована за нови период.", "points": "{n} поена",
		"updateTitle": "Изашао је gameWE {component} {version}", "updateBody": "Изашла је нова верзија gameWE {component}: {version}.", "running": "Инсталирана: {current}",
		"testTitle": "gameWE тест порука", "testBody": "Ова тест порука долази од gameWE колектора. Овако стижу обавештења преко {channel}.",
		"web": "веб", "collector": "колектор", "email": "е-пошта",
	},
	"zh-tw": {
		"resetTitle": "排行榜 {ranking} 已重設", "resetBody": "排行榜 {ranking} 已重設。", "by": "要求者：{by}",
		"hallTitle": "名人堂 {ranking}：{from} – {to}", "hallBody": "{ranking} 的期間 {from} – {to} 已結束。最佳玩家：",
		"nobody": "此期間沒有人遊玩。", "hallReset": "排行榜已為新期間重設。", "points": "{n} 分",
		"updateTitle": "gameWE {component} {version} 已發布", "updateBody": "gameWE {component} 有新版本：{version}。", "running": "已安裝：{current}",
		"testTitle": "gameWE 測試訊息", "testBody": "這則測試訊息來自 gameWE 收集器。透過 {channel} 的通知會像這樣送達。",
		"web": "網站", "collector": "收集器", "email": "電子郵件",
	},
	"zh-cn": {
		"resetTitle": "排行榜 {ranking} 已重置", "resetBody": "排行榜 {ranking} 已重置。", "by": "请求者：{by}",
		"hallTitle": "名人堂 {ranking}：{from} – {to}", "hallBody": "{ranking} 的周期 {from} – {to} 已结束。最佳玩家：",
		"nobody": "此周期内没有人玩。", "hallReset": "排行榜已为新周期重置。", "points": "{n} 分",
		"updateTitle": "gameWE {component} {version} 已发布", "updateBody": "gameWE {component} 有新版本：{version}。", "running": "已安装：{current}",
		"testTitle": "gameWE 测试消息", "testBody": "这条测试消息来自 gameWE 收集器。通过 {channel} 的通知会像这样到达。",
		"web": "网站", "collector": "收集器", "email": "电子邮件",
	},
	"ja": {
		"resetTitle": "ランキング {ranking} をリセットしました", "resetBody": "ランキング {ranking} がリセットされました。", "by": "実行者: {by}",
		"hallTitle": "殿堂 {ranking}: {from} – {to}", "hallBody": "{ranking} の期間 {from} – {to} が終了しました。上位プレイヤー:",
		"nobody": "この期間にプレイした人はいません。", "hallReset": "新しい期間に向けてランキングがリセットされました。", "points": "{n} ポイント",
		"updateTitle": "gameWE {component} {version} がリリースされました", "updateBody": "gameWE {component} の新しいバージョンが出ました: {version}。", "running": "インストール済み: {current}",
		"testTitle": "gameWE テストメッセージ", "testBody": "このテストメッセージは gameWE コレクターから送られています。{channel} の通知はこのように届きます。",
		"web": "Web", "collector": "コレクター", "email": "メール",
	},
	"ko": {
		"resetTitle": "랭킹 {ranking} 초기화됨", "resetBody": "랭킹 {ranking}이(가) 초기화되었습니다.", "by": "요청자: {by}",
		"hallTitle": "명예의 전당 {ranking}: {from} – {to}", "hallBody": "{ranking}의 기간 {from} – {to}이(가) 끝났습니다. 최고의 플레이어:",
		"nobody": "이 기간에는 아무도 플레이하지 않았습니다.", "hallReset": "새 기간을 위해 랭킹이 초기화되었습니다.", "points": "{n}점",
		"updateTitle": "gameWE {component} {version} 출시", "updateBody": "gameWE {component}의 새 버전이 나왔습니다: {version}.", "running": "설치됨: {current}",
		"testTitle": "gameWE 테스트 메시지", "testBody": "이 테스트 메시지는 gameWE 수집기에서 보냈습니다. {channel} 알림은 이렇게 도착합니다.",
		"web": "웹", "collector": "수집기", "email": "이메일",
	},
	"ar": {
		"resetTitle": "تمت إعادة تعيين التصنيف {ranking}", "resetBody": "تمت إعادة تعيين التصنيف {ranking}.", "by": "بطلب من: {by}",
		"hallTitle": "قاعة المشاهير {ranking}: {from} – {to}", "hallBody": "انتهت الفترة {from} – {to} من {ranking}. أفضل اللاعبين:",
		"nobody": "لم يلعب أحد في هذه الفترة.", "hallReset": "تمت إعادة تعيين التصنيف للفترة الجديدة.", "points": "{n} نقطة",
		"updateTitle": "صدر gameWE {component} {version}", "updateBody": "صدر إصدار جديد من gameWE {component}: {version}.", "running": "المثبت: {current}",
		"testTitle": "رسالة اختبار من gameWE", "testBody": "رسالة الاختبار هذه من مُجمِّع gameWE. هكذا تصل الإشعارات عبر {channel}.",
		"web": "الويب", "collector": "المُجمِّع", "email": "البريد الإلكتروني",
	},
	"tr": {
		"resetTitle": "{ranking} sıralaması sıfırlandı", "resetBody": "{ranking} sıralaması sıfırlandı.", "by": "İsteyen: {by}",
		"hallTitle": "Şöhretler salonu {ranking}: {from} – {to}", "hallBody": "{ranking} için {from} – {to} dönemi sona erdi. En iyi oyuncular:",
		"nobody": "Bu dönemde kimse oynamadı.", "hallReset": "Sıralama yeni dönem için sıfırlandı.", "points": "{n} puan",
		"updateTitle": "gameWE {component} {version} yayımlandı", "updateBody": "gameWE {component} için yeni sürüm çıktı: {version}.", "running": "Kurulu: {current}",
		"testTitle": "gameWE test mesajı", "testBody": "Bu test mesajı gameWE toplayıcısından geliyor. {channel} bildirimleri böyle gelir.",
		"web": "web", "collector": "toplayıcı", "email": "e-posta",
	},
	"it": {
		"resetTitle": "Classifica {ranking} azzerata", "resetBody": "La classifica {ranking} è stata azzerata.", "by": "Richiesto da: {by}",
		"hallTitle": "Albo d’oro {ranking}: {from} – {to}", "hallBody": "Il periodo {from} – {to} di {ranking} è terminato. I migliori giocatori:",
		"nobody": "In questo periodo non ha giocato nessuno.", "hallReset": "La classifica è stata azzerata per il nuovo periodo.", "points": "{n} punti",
		"updateTitle": "È uscito gameWE {component} {version}", "updateBody": "È uscita una nuova versione di gameWE {component}: {version}.", "running": "Installata: {current}",
		"testTitle": "Messaggio di prova di gameWE", "testBody": "Questo messaggio di prova arriva dal collector di gameWE. Le notifiche via {channel} arrivano così.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"uk": {
		"resetTitle": "Рейтинг {ranking} скинуто", "resetBody": "Рейтинг {ranking} було скинуто.", "by": "Запросив: {by}",
		"hallTitle": "Зала слави {ranking}: {from} – {to}", "hallBody": "Період {from} – {to} рейтингу {ranking} завершено. Найкращі гравці:",
		"nobody": "У цей період ніхто не грав.", "hallReset": "Рейтинг скинуто для нового періоду.", "points": "{n} очок",
		"updateTitle": "Вийшов gameWE {component} {version}", "updateBody": "Вийшла нова версія gameWE {component}: {version}.", "running": "Встановлена: {current}",
		"testTitle": "Тестове повідомлення gameWE", "testBody": "Це тестове повідомлення від колектора gameWE. Так надходять сповіщення через {channel}.",
		"web": "веб", "collector": "колектор", "email": "ел. пошта",
	},
	"cs": {
		"resetTitle": "Žebříček {ranking} vynulován", "resetBody": "Žebříček {ranking} byl vynulován.", "by": "Vyžádal: {by}",
		"hallTitle": "Síň slávy {ranking}: {from} – {to}", "hallBody": "Období {from} – {to} žebříčku {ranking} skončilo. Nejlepší hráči:",
		"nobody": "V tomto období nikdo nehrál.", "hallReset": "Žebříček byl vynulován pro nové období.", "points": "{n} bodů",
		"updateTitle": "Vyšel gameWE {component} {version}", "updateBody": "Vyšla nová verze gameWE {component}: {version}.", "running": "Nainstalováno: {current}",
		"testTitle": "Testovací zpráva gameWE", "testBody": "Tato testovací zpráva přichází z collectoru gameWE. Takto přicházejí oznámení přes {channel}.",
		"web": "web", "collector": "collector", "email": "e-mail",
	},
	"sv": {
		"resetTitle": "Rankning {ranking} nollställd", "resetBody": "Rankningen {ranking} har nollställts.", "by": "Begärd av: {by}",
		"hallTitle": "Hall of fame {ranking}: {from} – {to}", "hallBody": "Perioden {from} – {to} för {ranking} är slut. De bästa spelarna:",
		"nobody": "Ingen spelade under den här perioden.", "hallReset": "Rankningen har nollställts för den nya perioden.", "points": "{n} poäng",
		"updateTitle": "gameWE {component} {version} har släppts", "updateBody": "En ny version av gameWE {component} har släppts: {version}.", "running": "Installerad: {current}",
		"testTitle": "gameWE-testmeddelande", "testBody": "Det här testmeddelandet kommer från gameWE-insamlaren. Så här kommer aviseringar via {channel} fram.",
		"web": "webb", "collector": "insamlare", "email": "e-post",
	},
}

// Lang is the language of the messages: gamewe_notify_lang, else the site's
// gamewe_default_lang, else English.
func Lang(opts map[string]string) string {
	for _, k := range []string{"gamewe_notify_lang", "gamewe_default_lang"} {
		v := strings.ToLower(strings.TrimSpace(opts[k]))
		switch v {
		case "bar", "gsw", "nds", "swg":
			return "de"
		}
		if _, ok := texts[v]; ok {
			return v
		}
	}
	return "en"
}

// t is the text key in lang with the placeholders filled.
func t(lang, key string, kv ...string) string {
	s, ok := texts[lang][key]
	if !ok {
		s = texts["en"][key]
	}
	for i := 0; i+1 < len(kv); i += 2 {
		s = strings.ReplaceAll(s, "{"+kv[i]+"}", kv[i+1])
	}
	return s
}

// link is the line with a page of the web, "" without gamewe_site_url.
func link(opts map[string]string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(opts["gamewe_site_url"]), "/")
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return ""
	}
	return "\n\n" + base + path
}

// ResetMessage tells that ranking (its name, code game) was reset; by is
// who asked for it (an admin, "cli", ...), "" for nobody named.
func ResetMessage(opts map[string]string, game, ranking, by string) Message {
	l := Lang(opts)
	body := t(l, "resetBody", "ranking", ranking)
	if by != "" {
		body += "\n" + t(l, "by", "by", by)
	}
	return Message{Title: t(l, "resetTitle", "ranking", ranking), Body: body + link(opts, "/"+game)}
}

// Place is one player of the hall of fame.
type Place struct {
	Place  int
	Name   string
	Points int64
}

// HallMessage tells that the period [from, to) of a ranking ended with
// top in its hall of fame and the ranking was reset.
func HallMessage(opts map[string]string, game, ranking string, from, to time.Time, top []Place) Message {
	l := Lang(opts)
	f, e := from.Format(time.DateOnly), to.AddDate(0, 0, -1).Format(time.DateOnly)
	var b strings.Builder
	b.WriteString(t(l, "hallBody", "ranking", ranking, "from", f, "to", e))
	if len(top) == 0 {
		b.WriteString("\n" + t(l, "nobody"))
	}
	for _, p := range top {
		fmt.Fprintf(&b, "\n%d. %s (%s)", p.Place, oneLine(p.Name), t(l, "points", "n", fmt.Sprint(p.Points)))
	}
	b.WriteString("\n\n" + t(l, "hallReset"))
	return Message{Title: t(l, "hallTitle", "ranking", ranking, "from", f, "to", e), Body: b.String() + link(opts, "/"+game+"/hall-of-fame")}
}

// UpdateMessage tells that version of component (web or collector) is
// out; current is the running one ("" unknown), url the release page.
func UpdateMessage(opts map[string]string, component, version, current, url string) Message {
	l := Lang(opts)
	c := t(l, component)
	body := t(l, "updateBody", "component", c, "version", version)
	if current != "" {
		body += "\n" + t(l, "running", "current", current)
	}
	if url != "" {
		body += "\n\n" + url
	}
	return Message{Title: t(l, "updateTitle", "component", c, "version", version), Body: body}
}

// TestMessage is the message of the test button.
func TestMessage(opts map[string]string, channel string) Message {
	l := Lang(opts)
	name := map[string]string{Telegram: "Telegram", Ntfy: "ntfy"}[channel]
	if channel == Email {
		name = t(l, "email")
	}
	return Message{Title: t(l, "testTitle"), Body: t(l, "testBody", "channel", name)}
}
