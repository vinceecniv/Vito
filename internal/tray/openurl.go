package tray

import "vito/internal/webapp"

// OpenURL opens Vito's page: in the web app the user installed from it, and
// otherwise in an ordinary browser tab, where the browser and Vito's own
// notification can offer to install it (webapp.Browser says why not an --app
// window).
func OpenURL(url string) error { return webapp.Open(url) }
