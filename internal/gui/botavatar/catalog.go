package botavatar

import "strings"

// Builtin contains all 23 starter and gallery avatar definitions from
// DominikScholz/open-bot-avatar (MIT License).
var Builtin = []Definition{
	// Starters (7)
	{
		Version: 1, ID: "controller", Name: "Controller",
		Body: Body{Path: "M28 22C38 20 44 23 50 23C56 23 62 20 72 22C84 24 90 34 91 48C92 62 91 76 82 80C75 83 69 78 66 72C63 67 58 66 50 66C42 66 37 67 34 72C31 78 25 83 18 80C9 76 8 62 9 48C10 34 16 24 28 22Z"},
		Eyes: Eyes{X: 50, Y: 42, Spacing: 16, Scale: 1.05},
	},
	{
		Version: 1, ID: "barbell", Name: "Barbell",
		Body: Body{Path: "M12 41H12A4 4 0 0 1 16 45V55A4 4 0 0 1 12 59H12A4 4 0 0 1 8 55V45A4 4 0 0 1 12 41ZM18 29H18A4 4 0 0 1 22 33V67A4 4 0 0 1 18 71H18A4 4 0 0 1 14 67V33A4 4 0 0 1 18 29ZM26 17H26A5 5 0 0 1 31 22V78A5 5 0 0 1 26 83H26A5 5 0 0 1 21 78V22A5 5 0 0 1 26 17ZM38 33H62A10 10 0 0 1 72 43V57A10 10 0 0 1 62 67H38A10 10 0 0 1 28 57V43A10 10 0 0 1 38 33ZM74 17H74A5 5 0 0 1 79 22V78A5 5 0 0 1 74 83H74A5 5 0 0 1 69 78V22A5 5 0 0 1 74 17ZM82 29H82A4 4 0 0 1 86 33V67A4 4 0 0 1 82 71H82A4 4 0 0 1 78 67V33A4 4 0 0 1 82 29ZM88 41H88A4 4 0 0 1 92 45V55A4 4 0 0 1 88 59H88A4 4 0 0 1 84 55V45A4 4 0 0 1 88 41Z"},
		Eyes: Eyes{X: 50, Y: 50, Spacing: 15, Scale: 1.05},
	},
	{
		Version: 1, ID: "mug", Name: "Mug",
		Body: Body{Path: "M22 20H62C67 20 70 23 70 28V72C70 80 64 86 56 86H28C20 86 14 80 14 72V28C14 23 17 20 22 20ZM68 36C80 34 87 42 87 53C87 64 80 72 68 70ZM69 45L69 61C75.5 62 79 58.5 79 53C79 47.5 76 44 69 45Z"},
		Eyes: Eyes{X: 42, Y: 49, Spacing: 16, Scale: 1.15},
	},
	{
		Version: 1, ID: "lightbulb", Name: "Lightbulb",
		Body: Body{Path: "M36 72V66C36 60 18 55 18 40A32 32 0 0 1 82 40C82 55 64 60 64 66V72ZM36 68H64V77A4 4 0 0 1 60 81H40A4 4 0 0 1 36 77ZM43 78H57V84A4 4 0 0 1 53 88H47A4 4 0 0 1 43 84Z"},
		Eyes: Eyes{X: 50, Y: 40, Spacing: 17, Scale: 1.2},
	},
	{
		Version: 1, ID: "chat", Name: "Chat",
		Body: Body{Path: "M28 14H72C82 14 90 22 90 32V54C90 64 82 72 72 72H42L25 86C23.5 87.3 21.5 86 22.3 84L27 71.5C17 70 10 63 10 54V32C10 22 18 14 28 14Z"},
		Eyes: Eyes{X: 50, Y: 42, Spacing: 18, Scale: 1.25},
	},
	{
		Version: 1, ID: "sprout", Name: "Sprout",
		Body: Body{Path: "M49.5 19H50.5A3 3 0 0 1 53.5 22V46H46.5V22A3 3 0 0 1 49.5 19ZM49 38C36 41 22.5 36 17.6 23.4A1.4 1.4 0 0 1 18.4 21.6C29 17.5 41.5 21.5 49 34ZM51 30C57 14.5 72 8.5 82.6 11.8A1.4 1.4 0 0 1 83.5 13.4C80.5 27.5 66 35.5 51 34ZM24 44H76A4 4 0 0 1 80 48V54A4 4 0 0 1 76 58H24A4 4 0 0 1 20 54V48A4 4 0 0 1 24 44ZM24 56H76L71 84C70.4 87.5 68 90 64.5 90H35.5C32 90 29.6 87.5 29 84Z"},
		Eyes: Eyes{X: 50, Y: 71, Spacing: 16, Scale: 1.05},
	},
	{
		Version: 1, ID: "watering-can", Name: "Watering Can",
		Body: Body{Path: "M32 38H62C68 38 71 41 72 47L74 76C74.5 82 70 86 64 86H30C24 86 19.5 82 20 76L22 47C23 41 26 38 32 38ZM23 63C14 63 8.5 57 8.5 48C8.5 37 13 30.5 19 30.5C25 30.5 28.5 34 30 39.5L26 52ZM23.5 42C22 38 20 36 17.5 36C15 36 14 41 14 48C14 54 16 57.5 22 58L23.5 47ZM69 66C75 61 78 55 80 46.5L88 49.5C85 62 80 73 72 81ZM78.7 43A7 4.5 25 0 1 91.3 49A7 4.5 25 0 1 78.7 43Z"},
		Eyes: Eyes{X: 47, Y: 61, Spacing: 16, Scale: 1.15},
	},

	// Gallery (16)
	{
		Version: 1, ID: "book", Name: "Book",
		Body: Body{Path: "M12 32C12 29 14 27 17 26C30 21.5 42 23 50 31C58 23 70 21.5 83 26C86 27 88 29 88 32V64H89C90.7 64 92 65.3 92 67V75C92 77 90.5 78 88.5 77.5C76 75 60 76 50 84C40 76 24 75 11.5 77.5C9.5 78 8 77 8 75V67C8 65.3 9.3 64 11 64H12Z"},
		Eyes: Eyes{X: 50, Y: 51, Spacing: 18, Scale: 1.1},
	},
	{
		Version: 1, ID: "brain", Name: "Brain",
		Body: Body{Path: "M14 50C10 42 14 32 22 30C24 21 34 17 41 21C46 15 56 15 61 20C68 16 78 20 79 28C87 30 91 39 88 47C92 54 89 64 81 66C80 71 76 74 71 73C67 75 64 74 62 72C61 74 61 76 61.5 78C62 81 59 82.5 57 81.5C55.5 80.5 55 78 54 76C53 74 50 72.5 47 72.5C42 74 38 72 36 70C30 73 20 71 18 64C12 62 10 56 14 50Z"},
		Eyes: Eyes{X: 50, Y: 45, Spacing: 17, Scale: 1.15},
	},
	{
		Version: 1, ID: "bull", Name: "Bull",
		Body: Body{Path: "M40 28L60 28C70 28 76 36 76 48L76 62C76 74 66 84 50 84C34 84 24 74 24 62L24 48C24 36 30 28 40 28ZM32 40C22 38 15 32 15 24C15 18 18 13 22 10C25 8 28 10 27 13C25 18 24 22 28 26C31 29 36 30 42 30ZM58 30C64 30 69 29 72 26C76 22 75 18 73 13C72 10 75 8 78 10C82 13 85 18 85 24C85 32 78 38 68 40ZM29 55C23 56 17 55 12 52C9 50 8 47 10 45C14 41 22 40 28 42ZM72 42C78 40 86 41 90 45C92 47 91 50 88 52C83 55 77 56 71 55Z"},
		Eyes: Eyes{X: 50, Y: 52, Spacing: 17, Scale: 1.15},
	},
	{
		Version: 1, ID: "bunny", Name: "Bunny",
		Body: Body{Path: "M50 40C68 40 80 50 80 64C80 78 68 86 50 86C32 86 20 78 20 64C20 50 32 40 50 40ZM30 46C26 36 24 24 26 16C28 10 34 10 37 15C40 22 42 34 42 44ZM58 44C58 34 61 22 66 15C69 10 76 11 76 17C76 25 74 36 70 46Z"},
		Eyes: Eyes{X: 50, Y: 62, Spacing: 17, Scale: 1.15},
	},
	{
		Version: 1, ID: "camera", Name: "Camera",
		Body: Body{Path: "M17 34H20V31C20 29 21 28 23 28H27C29 28 30 29 30 31V34H36L40 23C41 21 42 20 44 20H56C58 20 59 21 60 23L64 34H83C87.5 34 91 37.5 91 42V72C91 76.5 87.5 80 83 80H17C12.5 80 9 76.5 9 72V42C9 37.5 12.5 34 17 34Z"},
		Eyes: Eyes{X: 50, Y: 54, Spacing: 17, Scale: 1.1},
	},
	{
		Version: 1, ID: "cat", Name: "Cat",
		Body: Body{Path: "M50 23C68 23 82 30 82 45C82 58 68 65 50 65C32 65 18 58 18 45C18 30 32 23 50 23ZM24 42C21 32 20 22 22 15C23 11 26 10 29 12C34 16 39 21 44 28L36 42ZM64 42L56 28C61 21 66 16 71 12C74 10 77 11 78 15C80 22 79 32 76 42ZM38 56L62 56C70 56 74 66 74 76C74 83 70 86 63 86L37 86C30 86 26 83 26 76C26 66 30 56 38 56ZM70 72C77 74 81 70 80 64C79 60 81 57 84 58C87 59 88.5 63 88 68C87 77 80 83 70 81Z"},
		Eyes: Eyes{X: 50, Y: 43, Spacing: 17, Scale: 1.1},
	},
	{
		Version: 1, ID: "cloud", Name: "Cloud",
		Body: Body{Path: "M24 76C14 76 9 69 9 61C9 52 15 46 23 45.5C25 33 36 24 49 24C61 24 70 31 73 41C83 41 91 49 91 59C91 69 83 76 74 76Z"},
		Eyes: Eyes{X: 50, Y: 54, Spacing: 17, Scale: 1.15},
	},
	{
		Version: 1, ID: "egg", Name: "Egg",
		Body: Body{Path: "M50 10C66 10 82 34 82 58C82 76 68 88 50 88C32 88 18 76 18 58C18 34 34 10 50 10Z"},
		Eyes: Eyes{X: 50, Y: 54, Spacing: 17, Scale: 1.2},
	},
	{
		Version: 1, ID: "ghost", Name: "Ghost",
		Body: Body{Path: "M24 50C24 28 35 12 50 12C65 12 76 28 76 50L77 80C77 85 73 88 68 88C63 88 61 84 59 81C57 84 54 88 50 88C46 88 43 84 41 81C39 84 37 88 32 88C27 88 23 85 23 80Z"},
		Eyes: Eyes{X: 50, Y: 42, Spacing: 16, Scale: 1.15},
	},
	{
		Version: 1, ID: "magnifier", Name: "Magnifier",
		Body: Body{Path: "M41 12A29 29 0 0 1 64.44 58.08L68.50 62.13A8 8 0 0 1 75.65 64.33L87.67 76.36A8 8 0 0 1 76.36 87.67L64.33 75.65A8 8 0 0 1 62.13 68.50L58.08 64.44A29 29 0 1 1 41 12Z"},
		Eyes: Eyes{X: 41, Y: 40, Spacing: 17, Scale: 1.15},
	},
	{
		Version: 1, ID: "paint-brush", Name: "Paint Brush",
		Body: Body{Path: "M77.2 8.5C80.3 8.7 82.3 10.2 83.4 13.1C84 14.5 83.3 16.2 82.3 17.6L65.9 40.8L56.2 33.7L73.2 11C74.2 9.6 75.6 8.5 77.2 8.5ZM55.4 26.8L72.8 39.4C74.2 40.4 74.5 42.2 73.5 43.6L66.6 53C65.6 54.4 63.8 54.7 62.4 53.7L45.1 41C43.7 40 43.4 38.2 44.4 36.8L51.2 27.4C52.2 26 54 25.8 55.4 26.8ZM48.4 38.7L63.7 49.8C67.9 57.1 66.8 65.9 61.8 72.9C54.7 82.6 41.2 86.6 25.2 85.1C22.2 85 20 82.9 21 79.9C24.6 71.4 21.1 57.1 28.2 47.4C33.3 40.4 40.8 37.4 48.4 38.7Z"},
		Eyes: Eyes{X: 44, Y: 59, Spacing: 16, Scale: 1.05},
	},
	{
		Version: 1, ID: "palette", Name: "Palette",
		Body: Body{Path: "M46 18C70 16 90 28 90 48C90 68 74 84 52 84C42 84 36 80 34 74C32 68 36 64 32 60C28 56 22 62 16 58C10 54 10 46 12 40C16 26 30 19 46 18ZM21 44A5 5 0 1 0 31 44A5 5 0 1 0 21 44Z"},
		Eyes: Eyes{X: 58, Y: 46, Spacing: 17, Scale: 1.15},
	},
	{
		Version: 1, ID: "rocket", Name: "Rocket",
		Body: Body{Path: "M46 10C48 8.5 52 8.5 54 10C64 17 71 29 71 44V72H29V44C29 29 36 17 46 10ZM29 56H71C80 61 86 70 86 80C86 84 83 86 79 85L72 83H28L21 85C17 86 14 84 14 80C14 70 20 61 29 56ZM42 80H58C59 86 55 90 50 92C45 90 41 86 42 80Z"},
		Eyes: Eyes{X: 50, Y: 46, Spacing: 15, Scale: 1.05},
	},
	{
		Version: 1, ID: "shield", Name: "Shield",
		Body: Body{Path: "M50 14C60 18 72 18 82 16Q86 16 86 20L86 46C86 64 72 78 54 86.5Q50 88.5 46 86.5C28 78 14 64 14 46L14 20Q14 16 18 16C28 18 40 18 50 14Z"},
		Eyes: Eyes{X: 50, Y: 42, Spacing: 17, Scale: 1.15},
	},
	{
		Version: 1, ID: "star", Name: "Star",
		Body: Body{Path: "M53.7 17.95L62 31.2Q64.1 34.6 68 35.6L83.1 39.3Q89.9 41 85.4 46.4L75.4 58.3Q72.8 61.4 73.1 65.4L74.2 81Q74.7 88 68.2 85.4L53.7 79.5Q50 78 46.3 79.5L31.8 85.4Q25.3 88 25.8 81L26.9 65.4Q27.2 61.4 24.6 58.3L14.6 46.4Q10.1 41 16.9 39.3L32 35.6Q35.9 34.6 38 31.2L46.3 17.95Q50 12 53.7 17.95Z"},
		Eyes: Eyes{X: 50, Y: 52, Spacing: 16, Scale: 1.1},
	},
	{
		Version: 1, ID: "witch-hat", Name: "Witch Hat",
		Body: Body{Path: "M50 72C72 72 92 75 92 80C92 85 72 87 50 87C28 87 8 85 8 80C8 75 28 72 50 72ZM37 30L63 30Q68 30 68.8 35L77.5 76Q78 80 74 80L26 80Q22 80 22.5 76L31.2 35Q32 30 37 30ZM30.56 38C33 26.5 40 18 52 14C62 10.5 72 9 81 11A3.2 3.2 0 0 1 81 17.4C74 18 69.5 24.5 68.1 31.5L69.44 38Z"},
		Eyes: Eyes{X: 50, Y: 54, Spacing: 15, Scale: 1.05},
	},
}

// DefaultColors maps every built-in avatar ID to its gallery color.
var DefaultColors = map[string]string{
	"controller":   "#ff6a1a",
	"barbell":      "#f2384b",
	"mug":          "#f4f4f4",
	"lightbulb":    "#f8a01c",
	"chat":         "#8b5cf6",
	"sprout":       "#1fbf68",
	"watering-can": "#14b3a5",
	"book":         "#a8693f",
	"brain":        "#ec4899",
	"bull":         "#f2384b",
	"bunny":        "#ec4899",
	"camera":       "#7a7a7a",
	"cat":          "#f8a01c",
	"cloud":        "#14b3a5",
	"egg":          "#f4f4f4",
	"ghost":        "#f4f4f4",
	"magnifier":    "#1f7cf2",
	"paint-brush":  "#1fbf68",
	"palette":      "#a8693f",
	"rocket":       "#7a7a7a",
	"shield":       "#1f7cf2",
	"star":         "#ff6a1a",
	"witch-hat":    "#8b5cf6",
}

// Lookup returns the built-in avatar with the given ID.
func Lookup(id string) (Definition, bool) {
	for _, d := range Builtin {
		if d.ID == id {
			return d, true
		}
	}
	return Definition{}, false
}

// DefaultColor returns the gallery default color for avatar ID.
func DefaultColor(id string) string {
	if c, ok := DefaultColors[id]; ok {
		return c
	}
	return DefaultColorHex
}

// agentAvatarDefaults maps known mako agent names to fitting default avatars
// and colors when the user leaves avatar selection on "Auto per agent".
var agentAvatarDefaults = map[string][2]string{
	"claude":      {"brain", "#d97757"},
	"codex":       {"rocket", "#10a37f"},
	"gemini":      {"star", "#4285f4"},
	"agy":         {"cloud", "#6366f1"},
	"antigravity": {"cloud", "#6366f1"},
	"cursor":      {"controller", "#3b82f6"},
	"opencode":    {"mug", "#14b3a5"},
	"aider":       {"lightbulb", "#f8a01c"},
	"copilot":     {"shield", "#8b5cf6"},
	"amp":         {"sprout", "#1fbf68"},
}

// ForAgent picks a consistent avatar definition and color for an agent name.
func ForAgent(agentName string) (Definition, string) {
	key := strings.ToLower(strings.TrimSpace(agentName))
	if pair, ok := agentAvatarDefaults[key]; ok {
		if def, ok := Lookup(pair[0]); ok {
			return def, pair[1]
		}
	}
	if key == "" {
		return Builtin[0], DefaultColor(Builtin[0].ID)
	}
	idx := int(HashString(key) % uint32(len(Builtin)))
	def := Builtin[idx]
	return def, DefaultColor(def.ID)
}
