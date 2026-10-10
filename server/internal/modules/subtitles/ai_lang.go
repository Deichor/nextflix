package subtitles

// AILanguage is a target language of the AI translation.
type AILanguage struct {
	Code   string `json:"code"`
	Name   string `json:"name"`   // English name, used in the prompt
	Native string `json:"native"` // shown in the settings
}

// aiProfileLang in the target list stands for the language of the watching profile.
const aiProfileLang = "profile"

var aiLanguages = []AILanguage{
	{"tr", "Turkish", "Türkçe"}, {"en", "English", "English"}, {"de", "German", "Deutsch"},
	{"fr", "French", "Français"}, {"es", "Spanish", "Español"}, {"it", "Italian", "Italiano"},
	{"pt", "Portuguese", "Português"}, {"nl", "Dutch", "Nederlands"}, {"ru", "Russian", "Русский"},
	{"uk", "Ukrainian", "Українська"}, {"pl", "Polish", "Polski"}, {"cs", "Czech", "Čeština"},
	{"sk", "Slovak", "Slovenčina"}, {"hu", "Hungarian", "Magyar"}, {"ro", "Romanian", "Română"},
	{"bg", "Bulgarian", "Български"}, {"el", "Greek", "Ελληνικά"}, {"sr", "Serbian", "Srpski"},
	{"hr", "Croatian", "Hrvatski"}, {"bs", "Bosnian", "Bosanski"}, {"sl", "Slovenian", "Slovenščina"},
	{"mk", "Macedonian", "Македонски"}, {"sq", "Albanian", "Shqip"}, {"sv", "Swedish", "Svenska"},
	{"da", "Danish", "Dansk"}, {"no", "Norwegian", "Norsk"}, {"fi", "Finnish", "Suomi"},
	{"is", "Icelandic", "Íslenska"}, {"et", "Estonian", "Eesti"}, {"lv", "Latvian", "Latviešu"},
	{"lt", "Lithuanian", "Lietuvių"}, {"ar", "Arabic", "العربية"}, {"fa", "Persian", "فارسی"},
	{"he", "Hebrew", "עברית"}, {"ur", "Urdu", "اردو"}, {"hi", "Hindi", "हिन्दी"},
	{"bn", "Bengali", "বাংলা"}, {"ta", "Tamil", "தமிழ்"}, {"te", "Telugu", "తెలుగు"},
	{"ml", "Malayalam", "മലയാളം"}, {"mr", "Marathi", "मराठी"}, {"pa", "Punjabi", "ਪੰਜਾਬੀ"},
	{"gu", "Gujarati", "ગુજરાતી"}, {"kn", "Kannada", "ಕನ್ನಡ"}, {"th", "Thai", "ไทย"},
	{"vi", "Vietnamese", "Tiếng Việt"}, {"id", "Indonesian", "Bahasa Indonesia"},
	{"ms", "Malay", "Bahasa Melayu"}, {"tl", "Filipino", "Filipino"}, {"zh", "Simplified Chinese", "中文"},
	{"ja", "Japanese", "日本語"}, {"ko", "Korean", "한국어"}, {"az", "Azerbaijani", "Azərbaycan"},
	{"ka", "Georgian", "ქართული"}, {"hy", "Armenian", "Հայերեն"}, {"kk", "Kazakh", "Қазақ"},
	{"uz", "Uzbek", "Oʻzbek"}, {"sw", "Swahili", "Kiswahili"}, {"af", "Afrikaans", "Afrikaans"},
	{"ca", "Catalan", "Català"}, {"eu", "Basque", "Euskara"}, {"gl", "Galician", "Galego"},
	{"cy", "Welsh", "Cymraeg"}, {"ga", "Irish", "Gaeilge"},
}

func aiLanguage(code string) (AILanguage, bool) {
	for _, l := range aiLanguages {
		if l.Code == code {
			return l, true
		}
	}
	return AILanguage{}, false
}

// parseAILangs keeps "profile" and the known codes, in order, without duplicates.
func parseAILangs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		c := normLang(v)
		if v == aiProfileLang {
			c = aiProfileLang
		} else if _, ok := aiLanguage(c); !ok {
			continue
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}
