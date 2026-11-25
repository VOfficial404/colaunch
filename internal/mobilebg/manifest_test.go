package mobilebg

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"slices"
	"testing"
)

func TestAndroidForegroundServiceManifest(t *testing.T) {
	data, err := os.ReadFile("mygo-plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct {
		Mobile struct {
			Android struct {
				Permissions []string
				Application []string
			}
		}
	}
	if err := json.Unmarshal(data, &plugin); err != nil {
		t.Fatal(err)
	}
	android := plugin.Mobile.Android
	for _, permission := range []string{"FOREGROUND_SERVICE", "FOREGROUND_SERVICE_DATA_SYNC", "POST_NOTIFICATIONS"} {
		if !slices.Contains(android.Permissions, permission) {
			t.Errorf("missing Android permission %s", permission)
		}
	}

	// MyGo injects these fragments inside an application in the Android namespace.
	var application struct {
		Services []struct {
			Name     string `xml:"http://schemas.android.com/apk/res/android name,attr"`
			Exported string `xml:"http://schemas.android.com/apk/res/android exported,attr"`
			Type     string `xml:"http://schemas.android.com/apk/res/android foregroundServiceType,attr"`
		} `xml:"service"`
	}
	manifest := []byte(`<application xmlns:android="http://schemas.android.com/apk/res/android">`)
	for _, fragment := range android.Application {
		manifest = append(manifest, fragment...)
	}
	manifest = append(manifest, "</application>"...)
	if err := xml.Unmarshal(manifest, &application); err != nil {
		t.Fatal(err)
	}
	for _, service := range application.Services {
		if service.Name != "dev.mygo.ColaunchBackgroundService" {
			continue
		}
		if service.Exported != "false" || service.Type != "dataSync" {
			t.Fatalf("service must be private and support dataSync: %+v", service)
		}
		return
	}
	t.Fatal("foreground service missing from the android.application fragments")
}
