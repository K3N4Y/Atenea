//go:build production

package dotenv

// Load is a no-op in production builds: a release binary must never pick up
// secrets from a .env in the working directory. Keys come from real
// environment variables or from the credentials stored by /connect.
func Load(string) {}
