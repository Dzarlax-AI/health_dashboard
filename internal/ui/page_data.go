package ui

// BasePage holds common data available to every page template.
type BasePage struct {
	Lang      string
	Title     string
	ActiveNav string // "dashboard", "metrics", "settings", "admin"
	IsAdmin   bool
}

// LangOption represents a language choice for the language switcher.
type LangOption struct {
	Code   string
	Label  string
	Active bool
}

// LangOptions returns the available languages with the current one marked active.
func LangOptions(current string) []LangOption {
	return []LangOption{
		{Code: "en", Label: "EN", Active: current == "en"},
		{Code: "ru", Label: "RU", Active: current == "ru"},
		{Code: "sr", Label: "SR", Active: current == "sr"},
	}
}
