package notify

import toast "git.sr.ht/~jackmordaunt/go-toast/v2"

// appID names the app in the Action Center, and activationGUID is the COM
// class Windows hands a clicked toast back through.
const (
	appID          = "mako"
	activationGUID = "{8D457AC6-C0C7-4A00-8C63-A0A26356346E}"
)

func setup() {
	if err := toast.SetAppData(toast.AppData{AppID: appID, GUID: activationGUID}); err != nil {
		return
	}
	toast.SetActivationCallback(func(target string, _ []toast.UserData) { click(target) })
}

func post(title, body, target string) {
	_ = (&toast.Notification{AppID: appID, Title: title, Body: body, ActivationArguments: target}).Push()
}
