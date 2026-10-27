package shellenv

// Load does nothing on Windows, where an app's environment is the user's.
func Load() {}
