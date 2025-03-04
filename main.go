package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io/ioutil"
	"math/big"
	"net"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cloudfoundry-community/vaultkv"
	"github.com/jhunt/go-ansi"
	fmt "github.com/jhunt/go-ansi"
	"github.com/jhunt/go-cli"
	env "github.com/jhunt/go-envirotron"
	"gopkg.in/yaml.v2"

	"github.com/starkandwayne/safe/prompt"
	"github.com/starkandwayne/safe/rc"
	"github.com/starkandwayne/safe/vault"

	uuid "github.com/pborman/uuid"
)

var Version string

func connect(auth bool) *vault.Vault {
	var caCertPool *x509.CertPool
	if os.Getenv("VAULT_CACERT") != "" {
		contents, err := ioutil.ReadFile(os.Getenv("VAULT_CACERT"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "@R{!! Could not read CA certificates: %s}", err.Error())
		}

		caCertPool = x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(contents)
	}

	shouldSkipVerify := func() bool {
		skipVerifyVal := os.Getenv("VAULT_SKIP_VERIFY")
		if skipVerifyVal != "" && skipVerifyVal != "false" {
			return true
		}
		return false
	}

	conf := vault.VaultConfig{
		URL:        getVaultURL(),
		Token:      os.Getenv("VAULT_TOKEN"),
		Namespace:  os.Getenv("VAULT_NAMESPACE"),
		SkipVerify: shouldSkipVerify(),
		CACerts:    caCertPool,
	}

	if auth && conf.Token == "" {
		fmt.Fprintf(os.Stderr, "@R{You are not authenticated to a Vault.}\n")
		fmt.Fprintf(os.Stderr, "Try @C{safe auth ldap}\n")
		fmt.Fprintf(os.Stderr, " or @C{safe auth github}\n")
		fmt.Fprintf(os.Stderr, " or @C{safe auth okta}\n")
		fmt.Fprintf(os.Stderr, " or @C{safe auth token}\n")
		fmt.Fprintf(os.Stderr, " or @C{safe auth userpass}\n")
		fmt.Fprintf(os.Stderr, " or @C{safe auth approle}\n")
		os.Exit(1)
	}

	v, err := vault.NewVault(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "@R{!! %s}\n", err)
		os.Exit(1)
	}
	return v
}

//Exits program with error if no Vault targeted
func getVaultURL() string {
	ret := os.Getenv("VAULT_ADDR")
	if ret == "" {
		fmt.Fprintf(os.Stderr, "@R{You are not targeting a Vault.}\n")
		fmt.Fprintf(os.Stderr, "Try @C{safe target https://your-vault alias}\n")
		fmt.Fprintf(os.Stderr, " or @C{safe target alias}\n")
		os.Exit(1)
	}
	return ret
}

type Options struct {
	Insecure     bool `cli:"-k, --insecure"`
	Version      bool `cli:"-v, --version"`
	Help         bool `cli:"-h, --help"`
	Clobber      bool `cli:"--clobber, --no-clobber"`
	SkipIfExists bool
	Quiet        bool `cli:"--quiet"`

	// Behavour of -T must chain through -- separated commands.  There is code
	// that relies on this.  Will default to $SAFE_TARGET if it exists, or
	// the current safe target otherwise.
	UseTarget string `cli:"-T, --target" env:"SAFE_TARGET"`

	HelpCommand    struct{} `cli:"help"`
	VersionCommand struct{} `cli:"version"`

	Envvars struct{} `cli:"envvars"`
	Targets struct {
		JSON bool `cli:"--json"`
	} `cli:"targets"`

	Status struct {
		ErrorIfSealed bool `cli:"-e, --err-sealed"`
	} `cli:"status"`

	Unseal struct{} `cli:"unseal"`
	Seal   struct{} `cli:"seal"`
	Env    struct {
		ForBash bool `cli:"--bash"`
		ForFish bool `cli:"--fish"`
		ForJSON bool `cli:"--json"`
	} `cli:"env"`

	Auth struct {
		Path string `cli:"-p, --path"`
		JSON bool   `cli:"--json"`
	} `cli:"auth, login"`

	Logout struct{} `cli:"logout"`
	Renew  struct{} `cli:"renew"`
	Ask    struct{} `cli:"ask"`
	Set    struct{} `cli:"set, write"`
	Paste  struct{} `cli:"paste"`
	Exists struct{} `cli:"exists, check"`

	Local struct {
		As     string `cli:"--as"`
		File   string `cli:"-f, --file"`
		Memory bool   `cli:"-m, --memory"`
		Port   int    `cli:"-p, --port"`
	} `cli:"local"`

	Init struct {
		Single    bool `cli:"-s, --single"`
		NKeys     int  `cli:"--keys"`
		Threshold int  `cli:"--threshold"`
		JSON      bool `cli:"--json"`
		Sealed    bool `cli:"--sealed"`
		NoMount   bool `cli:"--no-mount"`
		Persist   bool `cli:"--persist, --no-persist"`
	} `cli:"init"`

	Rekey struct {
		NKeys     int      `cli:"--keys, --num-unseal-keys"`
		Threshold int      `cli:"--threshold, --keys-to-unseal"`
		GPG       []string `cli:"--gpg"`
		Persist   bool     `cli:"--persist, --no-persist"`
	} `cli:"rekey"`

	Get struct {
		KeysOnly bool `cli:"--keys"`
		Yaml     bool `cli:"--yaml"`
	} `cli:"get, read, cat"`

	Versions struct{} `cli:"versions,revisions"`

	List struct {
		Single bool `cli:"-1"`
		Quick  bool `cli:"-q, --quick"`
	} `cli:"ls"`

	Paths struct {
		ShowKeys bool `cli:"--keys"`
		Quick    bool `cli:"-q, --quick"`
	} `cli:"paths"`

	Tree struct {
		ShowKeys   bool `cli:"--keys"`
		HideLeaves bool `cli:"-d, --hide-leaves"`
		Quick      bool `cli:"-q, --quick"`
	} `cli:"tree"`

	Target struct {
		JSON        bool     `cli:"--json"`
		Interactive bool     `cli:"-i, --interactive"`
		Strongbox   bool     `cli:"-s, --strongbox, --no-strongbox"`
		CACerts     []string `cli:"--ca-cert"`
		Namespace   string   `cli:"-n, --namespace"`

		Delete struct{} `cli:"delete, rm"`
	} `cli:"target"`

	Delete struct {
		Recurse bool `cli:"-R, -r, --recurse"`
		Force   bool `cli:"-f, --force"`
		Destroy bool `cli:"-D, -d, --destroy"`
		All     bool `cli:"-a, --all"`
	} `cli:"delete, rm"`

	Undelete struct {
		All bool `cli:"-a, --all"`
	} `cli:"undelete, unrm, urm"`

	Revert struct {
		Deleted bool `cli:"-d, --deleted"`
	} `cli:"revert"`

	Export struct {
		All     bool `cli:"-a, --all"`
		Deleted bool `cli:"-d, --deleted"`
		//These do nothing but are kept for backwards-compat
		OnlyAlive bool `cli:"-o, --only-alive"`
		Shallow   bool `cli:"-s, --shallow"`
	} `cli:"export"`

	Import struct {
		IgnoreDestroyed bool `cli:"-I, --ignore-destroyed"`
		IgnoreDeleted   bool `cli:"-i, --ignore-deleted"`
		Shallow         bool `cli:"-s, --shallow"`
	} `cli:"import"`

	Move struct {
		Recurse bool `cli:"-R, -r, --recurse"`
		Force   bool `cli:"-f, --force"`
		Deep    bool `cli:"-d, --deep"`
	} `cli:"move, rename, mv"`

	Copy struct {
		Recurse bool `cli:"-R, -r, --recurse"`
		Force   bool `cli:"-f, --force"`
		Deep    bool `cli:"-d, --deep"`
	} `cli:"copy, cp"`

	Gen struct {
		Policy string `cli:"-p, --policy"`
		Length int    `cli:"-l, --length"`
	} `cli:"gen, auto, generate"`

	SSH     struct{} `cli:"ssh"`
	RSA     struct{} `cli:"rsa"`
	DHParam struct{} `cli:"dhparam, dhparams, dh"`
	Prompt  struct{} `cli:"prompt"`
	Vault   struct{} `cli:"vault!"`
	Fmt     struct{} `cli:"fmt"`

	Curl struct {
		DataOnly bool `cli:"--data-only"`
	} `cli:"curl"`

	UUID   struct{} `cli:"uuid"`
	Option struct{} `cli:"option"`

	X509 struct {
		Validate struct {
			CA         bool     `cli:"-A, --ca"`
			SignedBy   string   `cli:"-i, --signed-by"`
			NotRevoked bool     `cli:"-R, --not-revoked"`
			Revoked    bool     `cli:"-r, --revoked"`
			NotExpired bool     `cli:"-E, --not-expired"`
			Expired    bool     `cli:"-e, --expired"`
			Name       []string `cli:"-n, --for"`
			Bits       []int    `cli:"-b, --bits"`
		} `cli:"validate, check"`

		Issue struct {
			CA           bool     `cli:"-A, --ca"`
			Subject      string   `cli:"-s, --subj, --subject"`
			Bits         int      `cli:"-b, --bits"`
			SignedBy     string   `cli:"-i, --signed-by"`
			Name         []string `cli:"-n, --name"`
			TTL          string   `cli:"-t, --ttl"`
			KeyUsage     []string `cli:"-u, --key-usage"`
			SigAlgorithm string   `cli:"-l, --sig-algorithm"`
		} `cli:"issue"`

		Revoke struct {
			SignedBy string `cli:"-i, --signed-by"`
		} `cli:"revoke"`

		Renew struct {
			Subject      string   `cli:"-s, --subj, --subject"`
			Name         []string `cli:"-n, --name"`
			SignedBy     string   `cli:"-i, --signed-by"`
			TTL          string   `cli:"-t, --ttl"`
			KeyUsage     []string `cli:"-u, --key-usage"`
			SigAlgorithm string   `cli:"-l, --sig-algorithm"`
		} `cli:"renew"`

		Reissue struct {
			Subject      string   `cli:"-s, --subj, --subject"`
			Name         []string `cli:"-n, --name"`
			Bits         int      `cli:"-b, --bits"`
			SignedBy     string   `cli:"-i, --signed-by"`
			TTL          string   `cli:"-t, --ttl"`
			KeyUsage     []string `cli:"-u, --key-usage"`
			SigAlgorithm string   `cli:"-l, --sig-algorithm"`
		} `cli:"reissue"`

		Show struct {
		} `cli:"show"`

		CRL struct {
			Renew bool `cli:"--renew"`
		} `cli:"crl"`
	} `cli:"x509"`
}

func main() {
	var opt Options
	opt.Gen.Policy = "a-zA-Z0-9"

	opt.Clobber = true

	opt.X509.Issue.Bits = 4096

	opt.Init.Persist = true
	opt.Rekey.Persist = true

	opt.Target.Strongbox = true

	go Signals()

	r := NewRunner()

	r.Dispatch("version", &Help{
		Summary: "Print the version of the safe CLI",
		Usage:   "safe version",
		Type:    AdministrativeCommand,
	}, func(command string, args ...string) error {
		if Version != "" {
			fmt.Fprintf(os.Stderr, "safe v%s\n", Version)
		} else {
			fmt.Fprintf(os.Stderr, "safe (development build)\n")
		}
		os.Exit(0)
		return nil
	})

	r.Dispatch("help", nil, func(command string, args ...string) error {
		if len(args) == 0 {
			args = append(args, "commands")
		}
		r.Help(os.Stderr, strings.Join(args, " "))
		os.Exit(0)
		return nil
	})

	r.Dispatch("envvars", nil, func(command string, args ...string) error {
		fmt.Printf(`@G{[SCRIPTING]}
  @B{SAFE_TARGET}    The vault alias which requests are sent to.

@G{[PROXYING]}
  @B{HTTP_PROXY}     The proxy to use for HTTP requests.
  @B{HTTPS_PROXY}    The proxy to use for HTTPS requests.
  @B{SAFE_ALL_PROXY} The proxy to use for both HTTP and HTTPS requests.
                 Overrides HTTP_PROXY and HTTPS_PROXY.
  @B{NO_PROXY}       A comma-separated list of domains to not use proxies for.
  @B{SAFE_KNOWN_HOSTS_FILE}
                 The location of your known hosts file, used for
                 'ssh+socks5://' proxying. Uses '${HOME}/.ssh/known_hosts'
                 by default.
  @B{SAFE_SKIP_HOST_KEY_VALIDATION}
                 If set, 'ssh+socks5://' proxying will skip host key validation
                 validation of the remote ssh server.


  The proxy environment variables support proxies with the schemes 'http://',
  'https://', 'socks5://', or 'ssh+socks5://'. http, https, and socks5 do what they
  say - they'll proxy through the server with the hostname:port given using the
  protocol specified in the scheme.

  'ssh+socks5://' will open an SSH tunnel to the given server, then will start a
  local SOCKS5 proxy temporarily which sends its traffic through the SSH tunnel.
  Because this requires an SSH connection, some extra information is required.
  This type of proxy should be specified in the form

      ssh+socks5://<user>@<hostname>:<port>/<path-to-private-key>
  or  ssh+socks5://<user>@<hostname>:<port>?private-key=<path-to-private-key

  If no port is provided, port 22 is assumed.
  Encrypted private keys are not supported. Password authentication is also not
  supported.

  Your known_hosts file is used to verify the remote ssh server's host key. If no
  key for the given server is present, you will be prompted to add the key. If no
  TTY when no host key is present, safe will return with a failure.

`)
		return nil
	})

	r.Dispatch("targets", &Help{
		Summary: "List all targeted Vaults",
		Usage:   "safe targets",
		Type:    AdministrativeCommand,
	}, func(command string, args ...string) error {
		if len(args) != 0 {
			r.ExitWithUsage("targets")
		}

		if opt.UseTarget != "" {
			fmt.Fprintf(os.Stderr, "@Y{Specifying --target to the targets command makes no sense; ignoring...}\n")
		}

		cfg := rc.Apply(opt.UseTarget)
		if opt.Targets.JSON {
			type vault struct {
				Name      string `json:"name"`
				URL       string `json:"url"`
				Verify    bool   `json:"verify"`
				Namespace string `json:"namespace,omitempty"`
				Strongbox bool   `json:"strongbox"`
			}
			vaults := make([]vault, 0)

			for name, details := range cfg.Vaults {
				vaults = append(vaults, vault{
					Name:      name,
					URL:       details.URL,
					Verify:    !details.SkipVerify,
					Namespace: details.Namespace,
					Strongbox: !details.NoStrongbox,
				})
			}
			b, err := json.MarshalIndent(vaults, "", "  ")
			if err != nil {
				return err
			}
			fmt.Printf("%s\n", string(b))
			return nil
		}

		wide := 0
		keys := make([]string, 0)
		for name := range cfg.Vaults {
			keys = append(keys, name)
			if len(name) > wide {
				wide = len(name)
			}
		}

		currentFmt := fmt.Sprintf("(*) @G{%%-%ds}\t@R{%%s} @Y{%%s}\n", wide)
		otherFmt := fmt.Sprintf("    %%-%ds\t@R{%%s} %%s\n", wide)
		hasCurrent := ""
		if cfg.Current != "" {
			hasCurrent = " - current target indicated with a (*)"
		}

		fmt.Fprintf(os.Stderr, "\nKnown Vault targets%s:\n", hasCurrent)
		sort.Strings(keys)
		for _, name := range keys {
			t := cfg.Vaults[name]
			skip := "           "
			if t.SkipVerify {
				skip = " (noverify)"
			} else if strings.HasPrefix(t.URL, "http:") {
				skip = " (insecure)"
			}
			format := otherFmt
			if name == cfg.Current {
				format = currentFmt
			}
			fmt.Fprintf(os.Stderr, format, name, skip, t.URL)
		}
		fmt.Fprintf(os.Stderr, "\n")
		return nil
	})

	r.Dispatch("target", &Help{
		Summary: "Target a new Vault, or set your current Vault target",
		Description: `Target a new Vault if URL and ALIAS are provided, or set
your current Vault target if just ALIAS is given. If the single argument form
if provided, the following flags are valid:

-k (--insecure) specifies to skip x509 certificate validation. This only has an
effect if the given URL uses an HTTPS scheme.

-s (--strongbox) specifies that the targeted Vault has a strongbox deployed at
its IP on port :8484. This is true by default. --no-strongbox will cause commands
that would otherwise use strongbox to run against only the targeted Vault.

-n (--namespace) specifies a Vault Enterprise namespace to run commands against
for this target.

--ca-cert can be either a PEM-encoded certificate value or filepath to a
PEM-encoded certificate. The given certificate will be trusted as the signing
certificate to the certificate served by the Vault server. This flag can be
provided multiple times to provide multiple CA certificates.
`,
		Usage: "safe [-k] [--[no]-strongbox] [-n] [--ca-cert] target [URL] [ALIAS] | safe target -i",
		Type:  AdministrativeCommand,
	}, func(command string, args ...string) error {
		var cfg rc.Config
		if !opt.Target.Interactive && len(args) == 0 {
			cfg = rc.Apply(opt.UseTarget)
		} else {
			cfg = rc.Read()
		}
		skipverify := false
		if os.Getenv("SAFE_SKIP_VERIFY") == "1" {
			skipverify = true
		}

		if opt.UseTarget != "" {
			fmt.Fprintf(os.Stderr, "@Y{Specifying --target to the target command makes no sense; ignoring...}\n")
		}

		printTarget := func() {
			u := cfg.URL()
			fmt.Fprintf(os.Stderr, "Currently targeting @C{%s} at @C{%s}\n", cfg.Current, u)
			if !cfg.Verified() {
				fmt.Fprintf(os.Stderr, "@R{Skipping TLS certificate validation}\n")
			}
			if cfg.Namespace() != "" {
				fmt.Fprintf(os.Stderr, "Using namespace @C{%s}\n", cfg.Namespace())
			}
			if cfg.HasStrongbox() {
				urlAsURL, err := url.Parse(u)
				fmt.Fprintf(os.Stderr, "Uses Strongbox")
				if err == nil {
					fmt.Fprintf(os.Stderr, " at @C{%s}", vault.StrongboxURL(urlAsURL))
				}
				fmt.Fprintf(os.Stderr, "\n")
			} else {
				fmt.Fprintf(os.Stderr, "Does not use Strongbox\n")
			}
			fmt.Fprintf(os.Stderr, "\n")
		}

		if opt.Target.Interactive {
			for {
				if len(cfg.Vaults) == 0 {
					fmt.Fprintf(os.Stderr, "@R{No Vaults have been targeted yet.}\n\n")
					fmt.Fprintf(os.Stderr, "You will need to target a Vault manually first.\n\n")
					fmt.Fprintf(os.Stderr, "Try something like this:\n")
					fmt.Fprintf(os.Stderr, "     @C{safe target ops https://address.of.your.vault}\n")
					fmt.Fprintf(os.Stderr, "     @C{safe auth (github|token|ldap|okta|userpass)}\n")
					fmt.Fprintf(os.Stderr, "\n")
					os.Exit(1)
				}
				r.Execute("targets")

				fmt.Fprintf(os.Stderr, "Which Vault would you like to target?\n")
				t := prompt.Normal("@G{> }")
				err := cfg.SetCurrent(t, skipverify)
				if err != nil {
					fmt.Fprintf(os.Stderr, "@R{%s}\n", err)
					continue
				}
				err = cfg.Write()
				if err != nil {
					return err
				}
				if !opt.Quiet {
					skip := ""
					if !cfg.Verified() {
						skip = " (skipping TLS certificate verification)"
					}
					fmt.Fprintf(os.Stderr, "Now targeting @C{%s} at @C{%s}@R{%s}\n\n", cfg.Current, cfg.URL(), skip)
				}
				return nil
			}
		}
		if len(args) == 0 {
			if !opt.Quiet {
				if opt.Target.JSON {
					var out struct {
						Name      string `json:"name"`
						URL       string `json:"url"`
						Verify    bool   `json:"verify"`
						Strongbox bool   `json:"strongbox"`
					}
					if cfg.Current != "" {
						out.Name = cfg.Current
						out.URL = cfg.URL()
						out.Verify = cfg.Verified()
						out.Strongbox = cfg.HasStrongbox()
					}
					b, err := json.MarshalIndent(&out, "", "  ")
					if err != nil {
						return err
					}
					fmt.Printf("%s\n", string(b))
					return nil
				}

				if cfg.Current == "" {
					fmt.Fprintf(os.Stderr, "@R{No Vault currently targeted}\n")
				} else {
					printTarget()
				}
			}
			return nil
		}
		if len(args) == 1 {
			err := cfg.SetCurrent(args[0], skipverify)
			if err != nil {
				return err
			}
			if !opt.Quiet {
				printTarget()
			}
			return cfg.Write()
		}

		if len(args) == 2 {
			var err error
			alias, url := args[0], args[1]
			if !(strings.HasPrefix(args[1], "http://") ||
				strings.HasPrefix(args[1], "https://")) {
				alias, url = url, alias
			}

			caCerts := []string{}
			for _, input := range opt.Target.CACerts {
				const errorPrefix = "Error reading CA certificates"
				p, _ := pem.Decode([]byte(input))
				// If not a PEM block, try to interpret it as a filepath pointing to
				// a file that contains a PEM block.
				if p == nil {
					pemData, err := ioutil.ReadFile(input)
					if err != nil {
						return fmt.Errorf("%s: While reading from file `%s': %s", errorPrefix, input, err.Error())
					}

					p, _ = pem.Decode([]byte(pemData))
					if p == nil {
						return fmt.Errorf("%s: File contents could not be parsed as PEM-encoded data", errorPrefix)
					}
				}

				_, err := x509.ParseCertificate(p.Bytes)
				if err != nil {
					return fmt.Errorf("%s: While parsing certificate ASN.1 DER data: %s", errorPrefix, err.Error())
				}

				toWrite := pem.EncodeToMemory(p)
				caCerts = append(caCerts, string(toWrite))
			}

			err = cfg.SetTarget(alias, rc.Vault{
				URL:         url,
				SkipVerify:  skipverify,
				NoStrongbox: !opt.Target.Strongbox,
				Namespace:   opt.Target.Namespace,
				CACerts:     caCerts,
			})
			if err != nil {
				return err
			}
			if !opt.Quiet {
				printTarget()
			}
			return cfg.Write()
		}

		r.ExitWithUsage("target")
		return nil
	})

	r.Dispatch("target delete", &Help{
		Summary: "Forget about a targeted Vault",
		Usage:   "safe target delete ALIAS",
		Type:    DestructiveCommand,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)
		if len(args) != 1 {
			r.ExitWithUsage("target delete")
		}

		delete(cfg.Vaults, args[0])
		if cfg.Current == args[0] {
			cfg.Current = ""
		}

		return cfg.Write()
	})

	r.Dispatch("status", &Help{
		Summary: "Print the status of the current target's backend nodes",
		Type:    AdministrativeCommand,
		Usage:   "safe status",
		Description: `
Returns the seal status of each node in the Vault cluster.

If strongbox is configured for this target, then strongbox is queried for seal
status of all nodes in the cluster. If strongbox is disabled for the target,
the /sys/health endpoint is queried for the target box to return the health of
just this Vault instance.

The following options are recognized:

	-e, --err-sealed  Causes safe to exit with a non-zero code if any of the
	                  queried Vaults are sealed.
		`,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)
		v := connect(false)

		type status struct {
			addr   string
			sealed bool
		}

		var statuses []status

		if cfg.HasStrongbox() {
			st, err := v.Strongbox()
			if err != nil {
				return fmt.Errorf("%s; are you targeting a `safe' installation?", err)
			}

			for addr, state := range st {
				statuses = append(statuses, status{addr, state == "sealed"})
			}
		} else {
			v.SetURL(cfg.URL())
			isSealed, err := v.Sealed()
			if err != nil {
				return err
			}

			statuses = append(statuses, status{cfg.URL(), isSealed})
		}

		var hasSealed bool

		for _, s := range statuses {
			if s.sealed {
				hasSealed = true
				fmt.Printf("@R{%s is sealed}\n", s.addr)
			} else {
				fmt.Printf("@G{%s is unsealed}\n", s.addr)
			}
		}

		if opt.Status.ErrorIfSealed && hasSealed {
			return fmt.Errorf("There are sealed Vaults")
		}

		return nil
	})

	r.Dispatch("local", &Help{
		Summary: "Run a local vault",
		Usage:   "safe local (--memory|--file path/to/dir) [--as name] [--port port]",
		Description: `
Spins up a new Vault instance.

By default, an unused port between 8201 and 9999 (inclusive) will be selected as
the Vault listening port. You may manually specify a port with the -p/--port 
flag. 

The new Vault will be initialized with a single seal key, targeted with
a catchy name, authenticated by the new root token, and populated with a
secret/handshake!

If you just need a transient Vault for testing or experimentation, and
don't particularly care about the contents of the Vault, specify the
--memory/-m flag and get an in-memory backend.

If, on the other hand, you want to keep the Vault around, possibly
spinning it down when not in use, specify the --file/-f flag, and give it
the path to a directory to use for the file backend.  The files created
by the mechanism will be encrypted.  You will be given the seal key for
subsequent activations of the Vault.
`,
		Type: AdministrativeCommand,
	}, func(command string, args ...string) error {
		if !opt.Local.Memory && opt.Local.File == "" {
			return fmt.Errorf("Please specify either --memory or --file <path>")
		}
		if opt.Local.Memory && opt.Local.File != "" {
			return fmt.Errorf("Please specify either --memory or --file <path>, but not both")
		}

		var port int
		if opt.Local.Port != 0 {
			port = opt.Local.Port
		} else {
			for port = 8201; port < 9999; port++ {
				conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if err != nil {
					break
				}
				conn.Close()
			}
		}

		f, err := ioutil.TempFile("", "kazoo")
		if err != nil {
			return err
		}
		fmt.Fprintf(f, `# safe local config
disable_mlock = true

listener "tcp" {
  address     = "127.0.0.1:%d"
  tls_disable = 1
}
`, port)

		//the "storage" configuration key was once called "backend"
		storageKey := "storage"
		cmd := exec.Command("vault", "version")
		versionOutput, err := cmd.CombinedOutput()
		if err == nil {
			matches := regexp.MustCompile("v([0-9]+)\\.([0-9]+)").FindSubmatch(versionOutput)
			if len(matches) >= 3 {
				major, err := strconv.ParseUint(string(matches[1]), 10, 64)
				if err != nil {
					goto doneVersionCheck
				}
				minor, err := strconv.ParseUint(string(matches[2]), 10, 64)
				if err != nil {
					goto doneVersionCheck
				}

				//if version < 0.8.0
				if major == 0 && minor < 8 {
					storageKey = "backend"
				}
			}
		} else {
			return fmt.Errorf("@R{Vault is not currently installed or located in $PATH}")
		}
	doneVersionCheck:

		keys := make([]string, 0)
		if opt.Local.Memory {
			fmt.Fprintf(f, "%s \"inmem\" {}\n", storageKey)
		} else {
			opt.Local.File = filepath.ToSlash(opt.Local.File)
			fmt.Fprintf(f, "%s \"file\" { path = \"%s\" }\n", storageKey, opt.Local.File)
			if _, err := os.Stat(opt.Local.File); err == nil || !os.IsNotExist(err) {
				keys = append(keys, pr("Unseal Key", false, true))
			}
		}

		echan := make(chan error)
		cmd = exec.Command("vault", "server", "-config", f.Name())
		cmd.Start()
		go func() {
			echan <- cmd.Wait()
		}()
		signal.Ignore(syscall.SIGINT)

		die := func(err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "@R{!! %s}\n", err)
			}
			fmt.Fprintf(os.Stderr, "@Y{shutting down the Vault...}\n")
			if err := cmd.Process.Kill(); err != nil {
				fmt.Fprintf(os.Stderr, "@R{NOTE: Unable to terminate the Vault process.}\n")
				fmt.Fprintf(os.Stderr, "@R{      You may have some environmental cleanup to do.}\n")
				fmt.Fprintf(os.Stderr, "@R{      Apologies.}\n")
			}
			os.Exit(1)
		}

		cfg := rc.Apply("")
		name := opt.Local.As
		if name == "" {
			name = RandomName()
			var n int
			for n = 15; n > 0; n-- {
				if existing, _ := cfg.Vault(name); existing == nil {
					break
				}
				name = RandomName()
			}
			if n == 0 {
				die(fmt.Errorf("I was unable to come up with a cool name for your local Vault.  Please try naming it with --as"))
			}
		} else {
			if existing, _ := cfg.Vault(name); existing != nil {
				die(fmt.Errorf("You already have '%s' as a Vault target", name))
			}
		}
		previous := cfg.Current

		cfg.SetTarget(name, rc.Vault{
			URL:         fmt.Sprintf("http://127.0.0.1:%d", port),
			SkipVerify:  false,
			NoStrongbox: true,
		})
		cfg.Write()

		rc.Apply("")
		v := connect(false)

		const maxStartupWait = 5 * time.Second
		const betweenChecksWait = 250 * time.Millisecond
		startupCheckBeginTime := time.Now()
		for {
			_, err := v.Sealed()
			if err == nil {
				break
			}

			if time.Since(startupCheckBeginTime) > maxStartupWait {
				die(fmt.Errorf("Timed out waiting for Vault to begin listening: %s", err))
			}

			time.Sleep(betweenChecksWait)
		}

		token := ""
		if len(keys) == 0 {
			keys, _, err = v.Init(1, 1)
			if err != nil {
				die(fmt.Errorf("Unable to initialize the new (temporary) Vault: %s", err))
			}
		}

		if err = v.Unseal(keys); err != nil {
			die(fmt.Errorf("Unable to unseal the new (temporary) Vault: %s", err))
		}
		token, err = v.NewRootToken(keys)
		if err != nil {
			die(fmt.Errorf("Unable to generate a new root token: %s", err))
		}

		cfg.SetToken(token)
		os.Setenv("VAULT_TOKEN", token)
		cfg.Write()
		v = connect(true)

		exists, err := v.MountExists("secret")
		if err != nil {
			return fmt.Errorf("Could not list mounts: %s", err)
		}

		if !exists {
			err := v.AddMount("secret", 2)
			if err != nil {
				return fmt.Errorf("Could not add `secret' mount: %s", err)
			}
			fmt.Printf("safe has mounted the @C{secret} backend\n\n")
		}

		s := vault.NewSecret()
		s.Set("knock", "knock", false)
		v.Write("secret/handshake", s)

		if !opt.Quiet {
			fmt.Fprintf(os.Stderr, "Now targeting (temporary) @Y{%s} at @C{%s}\n", cfg.Current, cfg.URL())
			if opt.Local.Memory {
				fmt.Fprintf(os.Stderr, "@R{This Vault is MEMORY-BACKED!}\n")
				fmt.Fprintf(os.Stderr, "If you want to @Y{retain your secrets} be sure to @C{safe export}.\n")
			} else {
				fmt.Fprintf(os.Stderr, "Storing data (encrypted) in @G{%s}\n", opt.Local.File)
				fmt.Fprintf(os.Stderr, "Your Vault Seal Key is @M{%s}\n", keys[0])
			}
			fmt.Fprintf(os.Stderr, "Ctrl-C to shut down the Vault\n")
		}

		err = <-echan
		fmt.Fprintf(os.Stderr, "Vault terminated normally, cleaning up...\n")
		cfg = rc.Apply("")
		if cfg.Current == name {
			cfg.Current = ""
			if _, found, _ := cfg.Find(previous); found {
				cfg.Current = previous
			}
		}
		delete(cfg.Vaults, name)
		cfg.Write()
		return err
	})

	r.Dispatch("init", &Help{
		Summary: "Initialize a new vault",
		Usage:   "safe init [--keys #] [--threshold #] [--single] [--json] [--no-mount] [--sealed]",
		Description: `
Initializes a brand new Vault backend, generating new seal keys, and an
initial root token.  This information will be printed out, so that you
can save it somewhere secure (encrypted drive, password manager, etc.)

By default, Vault is initialized with 5 unseal keys, 3 of which are
required to unseal the Vault after a restart.  You can adjust this via
the --keys and --threshold options.  The --single option is a shortcut
for specifying a single key and a threshold of 1.

Once the Vault is initialized, safe will unseal it automatically, using
the newly minted seal keys, unless you pass it the --sealed option.
The root token will also be stored in the ~/.saferc file, saving you the
trouble of calling 'safe auth token' yourself.

The --json flag causes 'safe init' to print out the seal keys and initial
root token in a machine-friendly JSON format, that looks like this:

    {
      "root_token": "05f28556-db0a-f76f-3c26-40de20f28cee"
      "seal_keys": [
        "jDuvcXg7s4QnjHjwN9ydSaFtoMj8YZWrO8hRFWT2PoqT",
        "XiE5cq0+AsUcK8EK8GomCsMdylixwWa8tM2L991OHcry",
        "F9NbroyispQTCMHBWBD5+lYxMEms5hntwsrxcdZx1+3w",
        "3scP3yIdfLv9mr0YbxZRClpPNSf5ohVpWmxrpRQ/a9JM",
        "NosOaAjZzvcdHKBvtaqLDRwWSG6/XkLwgZHvnIvAhOC5"
      ]
    }

This can be used to automate the setup of Vaults for test/dev purposes,
which can be quite handy.

By default, the seal keys will also be stored in the Vault itself,
unless you specify the --no-persist flag.  They will be written to
secret/vault/seal/keys, as key1, key2, ... keyN. Note that if
--sealed is also set, this option is ignored (since the Vault will
remain sealed).

In more recent versions of Vault, the "secret" mount is not mounted
by default. Safe will ensure that the mount is mounted anyway unless
the --no-mount option is given. The flag will not unmount an existing
secret mount in versions of Vault which mount "secret" by default.
Note that if --sealed is also set, this option is ignored (since the
Vault will remain sealed).

`,
		Type: AdministrativeCommand,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)
		v := connect(false)

		if opt.Init.NKeys == 0 {
			opt.Init.NKeys = 5
		}
		if opt.Init.Threshold == 0 {
			if opt.Init.NKeys > 3 {
				opt.Init.Threshold = opt.Init.NKeys - 2
			} else {
				opt.Init.Threshold = opt.Init.NKeys
			}
		}

		if opt.Init.Single {
			opt.Init.NKeys = 1
			opt.Init.Threshold = 1
		}

		/* initialize the vault */
		keys, token, err := v.Init(opt.Init.NKeys, opt.Init.Threshold)
		if err != nil {
			return err
		}

		if token == "" {
			panic("token was nil")
		}

		/* auth with the new root token, transparently */
		cfg.SetToken(token)
		if err := cfg.Write(); err != nil {
			return err
		}
		os.Setenv("VAULT_TOKEN", token)
		v = connect(true)

		/* be nice to the machines and machine-like intelligences */
		if opt.Init.JSON {
			out := struct {
				Keys  []string `json:"seal_keys"`
				Token string   `json:"root_token"`
			}{
				Keys:  keys,
				Token: token,
			}

			b, err := json.MarshalIndent(&out, "", "  ")
			if err != nil {
				return err
			}
			fmt.Printf("%s\n", string(b))
		} else {
			for i, key := range keys {
				fmt.Printf("Unseal Key #%d: @G{%s}\n", i+1, key)
			}
			fmt.Printf("Initial Root Token: @M{%s}\n", token)
			fmt.Printf("\n")
			if opt.Init.NKeys == 1 {
				fmt.Printf("Vault initialized with a single key. Please securely distribute it.\n")
				fmt.Printf("When the Vault is re-sealed, restarted, or stopped, you must provide\n")
				fmt.Printf("this key to unseal it again.\n")
				fmt.Printf("\n")
				fmt.Printf("Vault does not store the master key. Without the above unseal key,\n")
				fmt.Printf("your Vault will remain permanently sealed.\n")

			} else if opt.Init.NKeys == opt.Init.Threshold {
				fmt.Printf("Vault initialized with %d keys. Please securely distribute the\n", opt.Init.NKeys)
				fmt.Printf("above keys. When the Vault is re-sealed, restarted, or stopped,\n")
				fmt.Printf("you must provide all of these keys to unseal it again.\n")
				fmt.Printf("\n")
				fmt.Printf("Vault does not store the master key. Without all %d of the keys,\n", opt.Init.Threshold)
				fmt.Printf("your Vault will remain permanently sealed.\n")

			} else {
				fmt.Printf("Vault initialized with %d keys and a key threshold of %d. Please\n", opt.Init.NKeys, opt.Init.Threshold)
				fmt.Printf("securely distribute the above keys. When the Vault is re-sealed,\n")
				fmt.Printf("restarted, or stopped, you must provide at least %d of these keys\n", opt.Init.Threshold)
				fmt.Printf("to unseal it again.\n")
				fmt.Printf("\n")
				fmt.Printf("Vault does not store the master key. Without at least %d keys,\n", opt.Init.Threshold)
				fmt.Printf("your Vault will remain permanently sealed.\n")
			}

			fmt.Printf("\n")
		}

		if !opt.Init.Sealed {
			addrs := []string{}
			gotStrongbox := false
			if cfg.HasStrongbox() {
				if st, err := v.Strongbox(); err == nil {
					gotStrongbox = true
					for addr := range st {
						addrs = append(addrs, addr)
					}
				}
			}
			if !gotStrongbox {
				addrs = append(addrs, v.Client().Client.VaultURL.String())
			}

			for _, addr := range addrs {
				v.SetURL(addr)
				if err := v.Unseal(keys); err != nil {
					fmt.Fprintf(os.Stderr, "!!! unable to unseal newly-initialized vault (at %s): %s\n", addr, err)
				}
			}

			//Make a best attempt to wait until Vault has figured out which node should be the master.
			// This doesn't error out if no master comes forward, as there may be a cluster but no
			// Strongbox. In that case, it may error later, but we've done what we can.
			const maxAttempts = 5
			const waitInterval = 500 * time.Millisecond
			var currentAttempt int
		waitMaster:
			for currentAttempt < maxAttempts {
				for _, addr := range addrs {
					v.SetURL(addr)
					if err := v.Client().Client.Health(false); err == nil {
						break waitMaster
					}
				}
				currentAttempt++
				time.Sleep(waitInterval)
			}

			if !opt.Init.NoMount {
				exists, err := v.MountExists("secret")
				if err != nil {
					return fmt.Errorf("Could not list mounts: %s", err)
				}

				if !exists {
					err := v.AddMount("secret", 2)
					if err != nil {
						return fmt.Errorf("Could not add `secret' mount: %s", err)
					}

					if !opt.Init.JSON {
						fmt.Printf("safe has mounted the @C{secret} backend\n")
					}
				}
			}

			/* write secret/handshake, just for fun */
			s := vault.NewSecret()
			s.Set("knock", "knock", false)
			v.Write("secret/handshake", s)

			if !opt.Init.JSON {
				fmt.Printf("safe has unsealed the Vault for you, and written a test value\n")
				fmt.Printf("at @C{secret/handshake}.\n\n")
			}

			/* write seal keys to the vault */
			if opt.Init.Persist {
				v.SaveSealKeys(keys)
				if !opt.Init.JSON {
					fmt.Printf("safe has written the unseal keys at @C{secret/vault/seal/keys}\n")
				}
			}
		} else {
			if !opt.Init.JSON {
				fmt.Printf("Your Vault has been left sealed.\n")
			}
		}

		if !opt.Init.JSON {
			fmt.Printf("\n")
			fmt.Printf("You have been automatically authenticated to the Vault with the\n")
			fmt.Printf("initial root token.  Be safe out there!\n")
			fmt.Printf("\n")
		}

		return nil
	})

	r.Dispatch("unseal", &Help{
		Summary: "Unseal the current target",
		Usage:   "safe unseal",
		Type:    AdministrativeCommand,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)
		v := connect(false)

		var addrs []string
		if cfg.HasStrongbox() {
			st, err := v.Strongbox()
			if err != nil {
				return fmt.Errorf("%s; are you targeting a `safe' installation?", err)
			}

			for addr, state := range st {
				if state == "sealed" {
					addrs = append(addrs, addr)
				}
			}
		} else {
			v.SetURL(cfg.URL())
			isSealed, err := v.Sealed()
			if err != nil {
				return err
			}

			if isSealed {
				addrs = append(addrs, cfg.URL())
			}
		}

		if len(addrs) == 0 {
			fmt.Printf("@C{all vaults are already unsealed!}\n")
			return nil
		}

		v.SetURL(addrs[0])
		nkeys, err := v.SealKeys()
		if err != nil {
			return err
		}

		fmt.Printf("You need %d key(s) to unseal the vaults.\n\n", nkeys)
		keys := make([]string, nkeys)

		for i := 0; i < nkeys; i++ {
			keys[i] = pr(fmt.Sprintf("Key #%d", i+1), false, true)
		}

		for _, addr := range addrs {
			fmt.Printf("unsealing @G{%s}...\n", addr)
			v.SetURL(addr)
			err = v.Unseal(keys)
			if err != nil {
				return err
			}
		}

		return nil
	})

	r.Dispatch("seal", &Help{
		Summary: "Seal the current target",
		Usage:   "safe seal",
		Type:    AdministrativeCommand,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)
		v := connect(true)

		var toSeal []string
		if cfg.HasStrongbox() {
			st, err := v.Strongbox()
			if err != nil {
				return fmt.Errorf("%s; are you targeting a `safe' installation?", err)
			}

			for addr, state := range st {
				if state == "unsealed" {
					toSeal = append(toSeal, addr)
				}
			}
		} else {
			v.SetURL(cfg.URL())
			isSealed, err := v.Sealed()
			if err != nil {
				return nil
			}
			if !isSealed {
				toSeal = append(toSeal, cfg.URL())
			}
		}

		if len(toSeal) == 0 {
			fmt.Printf("@C{all vaults are already sealed!}\n")
		}

		consecutiveFailures := 0
		const maxFailures = 10
		const attemptInterval = 500 * time.Millisecond

		for len(toSeal) > 0 {
			for i, addr := range toSeal {
				v.SetURL(addr)
				err := v.Client().Client.Health(false)
				if err != nil {
					if vaultkv.IsErrStandby(err) {
						continue
					}

					return err
				}

				sealed, err := v.Seal()
				if err != nil {
					return err
				}

				if sealed {
					fmt.Printf("sealed @G{%s}...\n", addr)
					//Remove sealed Vault from list
					toSeal[i], toSeal[len(toSeal)-1] = toSeal[len(toSeal)-1], toSeal[i]
					toSeal = toSeal[:len(toSeal)-1]
					consecutiveFailures = 0
					break
				}
			}
			if len(toSeal) > 0 {
				consecutiveFailures++
				if consecutiveFailures == maxFailures {
					return fmt.Errorf("timed out waiting for leader election")
				}
				time.Sleep(attemptInterval)
			}
		}

		return nil
	})

	r.Dispatch("env", &Help{
		Summary: "Print the environment variables for the current target",
		Usage:   "safe env",
		Description: `
Print the environment variables representing the current target.

 --bash   Format the environment variables to be used by Bash.

 --fish   Format the environment variables to be used by fish.

 --json   Format the environment variables in json format.

Please note that if you specify --json, --bash or --fish then the output will be
written to STDOUT instead of STDERR to make it easier to consume.
		`,
		Type: AdministrativeCommand,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if opt.Env.ForBash && opt.Env.ForFish && opt.Env.ForJSON {
			r.Help(os.Stderr, "env")
			fmt.Fprintf(os.Stderr, "@R{Only specify one of --json, --bash OR --fish.}\n")
			os.Exit(1)
		}
		vars := map[string]string{
			"VAULT_ADDR":        os.Getenv("VAULT_ADDR"),
			"VAULT_TOKEN":       os.Getenv("VAULT_TOKEN"),
			"VAULT_SKIP_VERIFY": os.Getenv("VAULT_SKIP_VERIFY"),
			"VAULT_NAMESPACE":   os.Getenv("VAULT_NAMESPACE"),
		}

		switch {
		case opt.Env.ForBash:
			for name, value := range vars {
				if value != "" {
					fmt.Fprintf(os.Stdout, "\\export %s=%s;\n", name, value)
				} else {
					fmt.Fprintf(os.Stdout, "\\unset %s;\n", name)
				}
			}
		case opt.Env.ForFish:
			for name, value := range vars {
				if value == "" {
					fmt.Fprintf(os.Stdout, "set -u %s;\n", name)
				} else {
					fmt.Fprintf(os.Stdout, "set -x %s %s;\n", name, value)
				}
			}
		case opt.Env.ForJSON:
			jsonEnv := &struct {
				Addr  string `json:"VAULT_ADDR"`
				Token string `json:"VAULT_TOKEN,omitempty"`
				Skip  string `json:"VAULT_SKIP_VERIFY,omitempty"`
				NS    string `json:"VAULT_NAMESPACE,omitempty"`
			}{
				Addr:  vars["VAULT_ADDR"],
				Token: vars["VAULT_TOKEN"],
				Skip:  vars["VAULT_SKIP_VERIFY"],
				NS:    vars["VAULT_NAMESPACE"],
			}
			b, err := json.Marshal(jsonEnv)
			if err != nil {
				return err
			}
			fmt.Printf("%s\n", string(b))
			return nil

		default:
			for name, value := range vars {
				if value != "" {
					fmt.Fprintf(os.Stderr, "  @B{%s}  @G{%s}\n", name, value)
				}
			}
		}
		return nil
	})

	r.Dispatch("auth", &Help{
		Summary: "Authenticate to the current target",
		Usage:   "safe auth [--path <value>] (token|github|ldap|okta|userpass|approle)",
		Description: `
Set the authentication token sent when talking to the Vault.

Supported auth backends are:

token     Set the Vault authentication token directly.
github    Provide a Github personal access (oauth) token.
ldap      Provide LDAP user credentials.
okta      Provide Okta user credentials.
userpass  Provide a username and password registered with the UserPass backend.
approle   Provide a client ID and client secret registered with the AppRole backend.
status    Get information about current authentication status

Flags:
  -p, --path  Set the path of the auth backend mountpoint. For those who are
              familiar with the API, this is the part that comes after v1/auth.
              Defaults to the name of auth type (e.g. "userpass"), which is
              the default when creating auth backends with the Vault CLI.
  -j, --json  For auth status, returns the information as a JSON object.
`,
		Type: AdministrativeCommand,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)
		v := connect(false)
		v.Client().Client.SetAuthToken("")

		method := "token"
		if len(args) > 0 {
			method = args[0]
			args = args[1:]
		}

		var token string
		var err error
		url := os.Getenv("VAULT_ADDR")
		target := cfg.Current
		if opt.UseTarget != "" {
			target = opt.UseTarget
		}
		fmt.Fprintf(os.Stderr, "Authenticating against @C{%s} at @C{%s}\n", target, url)

		authMount := method
		if opt.Auth.Path != "" {
			authMount = opt.Auth.Path
		}

		switch method {
		case "token":
			if opt.Auth.Path != "" {
				return fmt.Errorf("Setting a custom path is not supported for token auth")
			}
			token = prompt.Secure("Token: ")

		case "ldap":
			username := prompt.Normal("LDAP username: ")
			password := prompt.Secure("Password: ")

			result, err := v.Client().Client.AuthLDAPMount(authMount, username, password)
			if err != nil {
				return err
			}
			token = result.ClientToken

		case "okta":
			username := prompt.Normal("Okta username: ")
			password := prompt.Secure("Password: ")

			result, err := v.Client().Client.AuthOktaMount(authMount, username, password)
			if err != nil {
				return err
			}
			token = result.ClientToken

		case "github":
			accessToken := prompt.Secure("Github Personal Access Token: ")

			result, err := v.Client().Client.AuthGithubMount(authMount, accessToken)
			if err != nil {
				return err
			}
			token = result.ClientToken

		case "userpass":
			username := prompt.Normal("Username: ")
			password := prompt.Secure("Password: ")

			result, err := v.Client().Client.AuthUserpassMount(authMount, username, password)
			if err != nil {
				return err
			}
			token = result.ClientToken

		case "approle":
			roleID := prompt.Normal("Role ID: ")
			secretID := prompt.Secure("Secret ID: ")

			result, err := v.Client().Client.AuthApproleMount(authMount, roleID, secretID)
			if err != nil {
				return err
			}
			token = result.ClientToken

		case "status":
			v := connect(false)
			tokenInfo, err := v.Client().Client.TokenInfoSelf()
			var tokenObj TokenStatus
			if err != nil {
				if !(vaultkv.IsForbidden(err) ||
					vaultkv.IsNotFound(err) ||
					vaultkv.IsBadRequest(err)) {
					return err
				}
			} else {
				tokenObj.info = *tokenInfo
				tokenObj.valid = true
			}

			var output string
			if opt.Auth.JSON {
				outputBytes, err := json.MarshalIndent(tokenObj, "", "  ")
				if err != nil {
					panic("Could not marshal json from TokenStatus object")
				}

				output = string(append(outputBytes, '\n'))
			} else {
				output = tokenObj.String()
			}

			fmt.Printf(output)
			return nil

		default:
			return fmt.Errorf("Unrecognized authentication method '%s'", method)
		}

		//This handles saving the token to the correct target when using the -T
		// flag to use a different target
		currentTarget := cfg.Current
		err = cfg.SetCurrent(target, false)
		if err != nil {
			return fmt.Errorf("Could not find target with name `%s'")
		}
		cfg.SetToken(token)
		cfg.SetCurrent(currentTarget, false)
		return cfg.Write()
	})

	r.Dispatch("logout", &Help{
		Summary: "Forget the authentication token of the currently targeted Vault",
		Usage:   "safe logout\n",
		Type:    AdministrativeCommand,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)
		cfg.SetToken("")
		err := cfg.Write()
		if err != nil {
			return err
		}

		target := cfg.Current
		if opt.UseTarget != "" {
			target = opt.UseTarget
		}
		fmt.Fprintf(os.Stderr, "Successfully logged out of @C{%s}\n", target)
		return nil
	})

	r.Dispatch("renew", &Help{
		Summary: "Renew one or more authentication tokens",
		Usage:   "safe renew [all]\n",
		Type:    AdministrativeCommand,
	}, func(command string, args ...string) error {
		if len(args) > 0 {
			if len(args) != 1 || args[0] != "all" {
				r.ExitWithUsage("renew")
			}
			cfg := rc.Apply("")
			failed := 0
			for vault := range cfg.Vaults {
				rc.Apply(vault)
				if os.Getenv("VAULT_TOKEN") == "" {
					fmt.Printf("skipping @C{%s} - no token found.\n", vault)
					continue
				}
				fmt.Printf("renewing token against @C{%s}...\n", vault)
				v := connect(true)
				if err := v.RenewLease(); err != nil {
					fmt.Fprintf(os.Stderr, "@R{failed to renew token against %s: %s}\n", vault, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("failed to renew %d token(s)", failed)
			}
			return nil
		}

		rc.Apply(opt.UseTarget)
		v := connect(true)
		if err := v.RenewLease(); err != nil {
			return err
		}
		return nil
	})

	writeHelper := func(prompt bool, insecure bool, command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if len(args) < 2 {
			r.ExitWithUsage(command)
		}
		v := connect(true)
		path, args := args[0], args[1:]
		s, err := v.Read(path)
		if err != nil && !vault.IsNotFound(err) {
			return err
		}
		exists := (err == nil)
		clobberKeys := []string{}
		for _, arg := range args {
			k, v, missing, err := parseKeyVal(arg, opt.Quiet)
			if err != nil {
				return err
			}
			if opt.SkipIfExists && exists && s.Has(k) {
				clobberKeys = append(clobberKeys, k)
				continue
			}
			// realize that we're going to fail, and don't prompt the user for any info
			if len(clobberKeys) > 0 {
				continue
			}
			if missing {
				v = pr(k, prompt, insecure)
			}
			if err != nil {
				return err
			}
			err = s.Set(k, v, opt.SkipIfExists)
			if err != nil {
				return err
			}
		}
		if len(clobberKeys) > 0 {
			if !opt.Quiet {
				fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to update} @C{%s}@R{, as the following keys would be clobbered:} @C{%s}\n",
					path, strings.Join(clobberKeys, ", "))
			}
			return nil
		}
		return v.Write(path, s)
	}

	r.Dispatch("ask", &Help{
		Summary: "Create or update an insensitive configuration value",
		Usage:   "safe ask PATH NAME=[VALUE] [NAME ...]",
		Type:    DestructiveCommand,
		Description: `
Update a single path in the Vault with new or updated named attributes.
Any existing name/value pairs not specified on the command-line will
be left alone, with their original values.

You will be prompted to provide (without confirmation) any values that
are omitted. Unlike the 'safe set' and 'safe paste' commands, data entry
is NOT obscured.
`,
	}, func(command string, args ...string) error {
		return writeHelper(false, false, "ask", args...)
	})

	r.Dispatch("set", &Help{
		Summary: "Create or update a secret",
		Usage:   "safe set PATH NAME=[VALUE] [NAME ...]",
		Type:    DestructiveCommand,
		Description: `
Update a single path in the Vault with new or updated named attributes.
Any existing name/value pairs not specified on the command-line will be
left alone, with their original values.

Values can be provided a number of different ways.

    safe set secret/path key=value

Will set "key" to "value", but that exposes the value in the process table
(and possibly in shell history files).  This is normally fine for usernames,
IP addresses, and other public information.

If this worries you, leave off the '=value', and safe will prompt you.

    safe set secret/path key

Some secrets perfer to live on disk, in files.  Certificates, private keys,
really long secrets that are tough to type, etc.  For those, you can use
the '@' notation:

    safe set secret/path key@path/to/file

This causes safe to read the file 'path/to/file', relative to the current
working directory, and insert the contents into the Vault.
`,
	}, func(command string, args ...string) error {
		return writeHelper(true, true, "set", args...)
	})

	r.Dispatch("paste", &Help{
		Summary: "Create or update a secret",
		Usage:   "safe paste PATH NAME=[VALUE] [NAME ...]",
		Type:    DestructiveCommand,
		Description: `
Works just like 'safe set', updating a single path in the Vault with new or
updated named attributes.  Any existing name/value pairs not specified on the
command-line will be left alone, with their original values.

You will be prompted to provide any values that are omitted, but unlike the
'safe set' command, you will not be asked to confirm those values.  This makes
sense when you are pasting in credentials from an external password manager
like 1password or Lastpass.
`,
	}, func(command string, args ...string) error {
		//Dispatch call.
		return writeHelper(false, true, "paste", args...)
	})

	r.Dispatch("exists", &Help{
		Summary: "Check to see if a secret exists in the Vault",
		Usage:   "safe exists PATH",
		Type:    NonDestructiveCommand,
		Description: `
When you want to see if a secret has been defined, but don't need to know
what its value is, you can use 'safe exists'.  PATH can either be a partial
path (i.e. 'secret/accounts/users/admin') or a fully-qualified path that
incudes a name (like 'secret/accounts/users/admin:username').

'safe exists' does not produce any output, and is suitable for use in scripts.

The process will exit 0 (zero) if PATH exists in the current Vault.
Otherwise, it will exit 1 (one).  If unrelated errors, like network timeouts,
certificate validation failure, etc. occur, they will be printed as well.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if len(args) != 1 {
			r.ExitWithUsage("exists")
		}
		v := connect(true)
		_, err := v.Read(args[0])
		if err != nil {
			if vault.IsNotFound(err) {
				os.Exit(1)
			}
			return err
		}
		os.Exit(0)
		return nil
	})

	r.Dispatch("get", &Help{
		Summary: "Retrieve the key/value pairs (or just keys) of one or more paths",
		Usage:   "safe get [--keys] [--yaml] PATH [PATH ...]",
		Description: `
Allows you to retrieve one or more values stored in the given secret, or just the
valid keys.  It operates in the following modes:

If a single path is specified that does not include a :key suffix, the output
will be the key:value pairs for that secret, in YAML format.  It will not include
the specified path as the base hash key; instead, it will be output as a comment
behind the document indicator (---).  To force it to include the full path as
the root key, specify --yaml.

If a single path is specified including the :key suffix, the single value of that
path:key will be output in string format.  To force the use of the fully qualified
{path: {key: value}} output in YAML format, use --yaml option.

If a single path is specified along with --keys, the list of keys for that given
path will be returned.  If that path does not contain any secrets (ie its not a
leaf node or does not exist), it will output nothing, but will not error.  If a
specific key is specified, it will output only that key if it exists, otherwise
nothing. You can specify --yaml to force YAML output.

If you specify more than one path, output is forced to be YAML, with the primary
hash key being the requested path (not including the key if provided).  If --keys
is specified, the next level will contain the keys found under that path; if the
path included a key component, only the specified keys will be present.  Without
the --keys option, the key: values for each found (or requested) key for the path
will be output.

If an invalid key or path is requested, an error will be output and nothing else
unless the --keys option is specified.  In that case, the error will be displayed
as a warning, but the output will be provided with an empty array for missing
paths/keys.
`,
		Type: NonDestructiveCommand,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if len(args) < 1 {
			r.ExitWithUsage("get")
		}

		v := connect(true)

		// Recessive case of one path
		if len(args) == 1 && !opt.Get.Yaml {
			s, err := v.Read(args[0])
			if err != nil {
				return err
			}

			if opt.Get.KeysOnly {
				keys := s.Keys()
				for _, key := range keys {
					fmt.Printf("%s\n", key)
				}
			} else if _, key, _ := vault.ParsePath(args[0]); key != "" {
				value, err := s.SingleValue()
				if err != nil {
					return err
				}
				fmt.Printf("%s\n", value)
			} else {
				fmt.Printf("--- # %s\n%s\n", args[0], s.YAML())
			}
			return nil
		}

		// Track errors, paths, keys, values
		errs := make([]error, 0)
		results := make(map[string]map[string]string, 0)
		missingKeys := make(map[string][]string)
		for _, path := range args {
			p, k, _ := vault.ParsePath(path)
			s, err := v.Read(path)

			// Check if the desired path[:key] is found
			if err != nil {
				errs = append(errs, err)
				if k != "" {
					if _, ok := missingKeys[p]; !ok {
						missingKeys[p] = make([]string, 0)
					}
					missingKeys[p] = append(missingKeys[p], k)
				}
				continue
			}

			if _, ok := results[p]; !ok {
				results[p] = make(map[string]string, 0)
			}
			for _, key := range s.Keys() {
				results[p][key] = s.Get(key)
			}
		}

		// Handle any errors encountered.  Warn for key request, return error otherwise
		var err error
		numErrs := len(errs)
		if numErrs == 1 {
			err = errs[0]
		} else if len(errs) > 1 {
			errStr := "Multiple errors found:"
			for _, err := range errs {
				errStr += fmt.Sprintf("\n   - %s", err)
			}
			err = errors.New(errStr)
		}
		if numErrs > 0 {
			if opt.Get.KeysOnly {
				fmt.Fprintf(os.Stderr, "@y{WARNING:} %s\n", err)
			} else {
				return err
			}
		}

		// Now that we've collected/collated all the data, format and print it
		fmt.Printf("---\n")
		if opt.Get.KeysOnly {
			printedPaths := make(map[string]bool, 0)
			for _, path := range args {
				p, _, _ := vault.ParsePath(path)
				if printed, _ := printedPaths[p]; printed {
					continue
				}
				printedPaths[p] = true
				result, ok := results[p]
				if !ok {
					yml, _ := yaml.Marshal(map[string][]string{p: []string{}})
					fmt.Printf("%s", string(yml))
				} else {
					foundKeys := reflect.ValueOf(result).MapKeys()
					strKeys := make([]string, len(foundKeys))
					for i := 0; i < len(foundKeys); i++ {
						strKeys[i] = foundKeys[i].String()
					}
					sort.Strings(strKeys)
					yml, _ := yaml.Marshal(map[string][]string{p: strKeys})
					fmt.Printf("%s\n", string(yml))
				}
			}
		} else {
			yml, _ := yaml.Marshal(results)
			fmt.Printf("%s\n", string(yml))
		}
		return nil
	})

	r.Dispatch("versions", &Help{
		Summary: "Print information about the versions of one or more paths",
		Usage:   "safe versions PATH [PATHS...]",
		Type:    NonDestructiveCommand,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		v := connect(true)

		if len(args) == 0 {
			return fmt.Errorf("No paths given")
		}

		for i := range args {
			_, _, version := vault.ParsePath(args[i])
			if version > 0 {
				return fmt.Errorf("Specifying version to versions is not supported")
			}
			versions, err := v.Client().Versions(args[i])
			if vaultkv.IsNotFound(err) {
				err = vault.NewSecretNotFoundError(args[i])
			}
			if err != nil {
				return err
			}

			if len(args) > 1 {
				fmt.Printf("@B{%s}:\n", args[i])
			}

			const numColumns = 3
			table := table{}

			table.setHeader("version", "status", "created at")

			for j := range versions {
				//Destroyed needs to be first because things can come back as both deleted _and_ destroyed.
				// destroyed is objectively more interesting.
				statusString := "@G{alive}"
				if versions[j].Destroyed {
					statusString = "@R{destroyed}"
				} else if versions[j].Deleted {
					statusString = "@Y{deleted}"
				}

				createdAtString := "unknown"

				if !versions[j].CreatedAt.IsZero() {
					createdAtString = versions[j].CreatedAt.Local().Format(time.RFC822)
				}

				table.addRow(
					fmt.Sprintf("%d", versions[j].Version),
					fmt.Sprintf(statusString),
					createdAtString,
				)
			}

			table.print()

			if len(args) > 1 && i != len(args)-1 {
				fmt.Printf("\n")
			}
		}

		return nil
	})

	r.Dispatch("ls", &Help{
		Summary: "Print the keys and sub-directories at one or more paths",
		Usage:   "safe ls [-1|-q] [PATH ...]",
		Type:    NonDestructiveCommand,
		Description: `
	Specifying the -1 flag will print one result per line.
	Specifying the -q flag will show secrets which have been marked as deleted.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		v := connect(true)
		display := func(paths []string) {
			if opt.List.Single {
				for _, s := range paths {
					if strings.HasSuffix(s, "/") {
						fmt.Printf("@B{%s}\n", s)
					} else {
						fmt.Printf("@G{%s}\n", s)
					}
				}
			} else {
				for _, s := range paths {
					if strings.HasSuffix(s, "/") {
						fmt.Printf("@B{%s}  ", s)
					} else {
						fmt.Printf("@G{%s}  ", s)
					}
				}
				fmt.Printf("\n")
			}
		}

		if len(args) == 0 {
			args = []string{"/"}
		}

		for _, path := range args {
			var paths []string
			if path == "" || path == "/" {
				generics, err := v.Mounts("generic")
				if err != nil {
					return err
				}
				kvs, err := v.Mounts("kv")
				if err != nil {
					return err
				}

				paths = append(generics, kvs...)
			} else {
				var err error
				paths, err = v.List(path)
				if err != nil {
					return err
				}
			}

			filteredPaths := []string{}
			if !opt.List.Quick {
				for i := range paths {
					if !strings.HasSuffix(paths[i], "/") {
						fullpath := path + "/" + paths[i]
						mountVersion, err := v.MountVersion(fullpath)
						if err != nil {
							return err
						}

						if mountVersion == 2 {
							_, err := v.Read(fullpath)
							if err != nil {
								if vault.IsNotFound(err) {
									continue
								}

								return err
							}
						}
					}
					filteredPaths = append(filteredPaths, paths[i])
				}
			} else {
				filteredPaths = paths
			}

			sort.Strings(filteredPaths)

			if len(args) != 1 {
				fmt.Printf("@C{%s}:\n", path)
			}
			display(filteredPaths)
			if len(args) != 1 {
				fmt.Printf("\n")
			}
		}
		return nil
	})

	r.Dispatch("tree", &Help{
		Summary: "Print a tree listing of one or more paths",
		Usage:   "safe tree [-d|-q|--keys] [PATH ...]",
		Type:    NonDestructiveCommand,
		Description: `
Walks the hierarchy of secrets stored underneath a given path, listing all
reachable name/value pairs and displaying them in a tree format.  If '-d' is
given, only the containing folders will be printed; this more concise output
can be useful when you're trying to get your bearings. If '-q' is given, safe
will not inspect each key in a v1 v2 mount backend to see if it has been marked
as deleted. This may cause keys which would 404 in an attempt to read them to
appear in the tree, but is often considerably quicker for larger vaults. This
flag does nothing for kv v1 mounts.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if opt.Tree.HideLeaves && opt.Tree.ShowKeys {
			return fmt.Errorf("Cannot specify both -d and --keys at the same time")
		}
		if len(args) == 0 {
			args = append(args, "secret")
		}
		r1, _ := regexp.Compile("^ ")
		r2, _ := regexp.Compile("^└")
		v := connect(true)
		for i, path := range args {
			secrets, err := v.ConstructSecrets(path, vault.TreeOpts{
				FetchKeys:           opt.Tree.ShowKeys,
				AllowDeletedSecrets: opt.Tree.Quick,
			})

			if err != nil {
				return err
			}
			lines := strings.Split(secrets.Draw(path, fmt.CanColorize(os.Stdout), !opt.Tree.HideLeaves), "\n")
			if i > 0 {
				lines = lines[1:] // Drop root '.' from subsequent paths
			}
			if i < len(args)-1 {
				lines = lines[:len(lines)-1]
			}
			for _, line := range lines {
				if i < len(args)-1 {
					line = r1.ReplaceAllString(r2.ReplaceAllString(line, "├"), "│")
				}
				fmt.Printf("%s\n", line)
			}
		}
		return nil
	})

	r.Dispatch("paths", &Help{
		Summary: "Print all of the known paths, one per line",
		Usage:   "safe paths [-q|--keys] PATH [PATH ...]",
		Type:    NonDestructiveCommand,
		Description: `
Walks the hierarchy of secrets stored underneath a given path, listing all
reachable name/value pairs and displaying them in a list. If '-q' is given,
safe will not inspect each key in a v1 v2 mount backend to see if it has been
marked as deleted. This may cause keys which would 404 in an attempt to read
them to appear in the tree, but is often considerably quicker for larger
vaults. This flag does nothing for kv v1 mounts.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if len(args) < 1 {
			args = append(args, "secret")
		}
		v := connect(true)
		for _, path := range args {
			secrets, err := v.ConstructSecrets(path, vault.TreeOpts{
				FetchKeys:           opt.Paths.ShowKeys,
				AllowDeletedSecrets: opt.Paths.Quick,
				SkipVersionInfo:     !opt.Paths.ShowKeys,
			})
			if err != nil {
				return err
			}

			fmt.Printf(strings.Join(secrets.Paths(), "\n"))
			fmt.Printf("\n")
		}
		return nil
	})

	r.Dispatch("delete", &Help{
		Summary: "Remove one or more path from the Vault",
		Usage:   "safe delete [-rfDa] PATH [PATH ...]",
		Type:    DestructiveCommand,
		Description: `
-d (--destroy) will cause KV v2 secrets to be destroyed instead of
being marked as deleted. For KV v1 backends, this would do nothing.
-a (--all) will delete (or destroy) all versions of the secret instead
of just the specified (or latest if unspecified) version.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) < 1 {
			r.ExitWithUsage("delete")
		}
		v := connect(true)

		verb := "delete"
		if opt.Delete.Destroy {
			verb = "destroy"
		}

		for _, path := range args {
			_, key, version := vault.ParsePath(path)

			//Ignore -r if path has a version or key because that seems like a mistake
			if opt.Delete.Recurse && (key == "" || version > 0) {
				if !opt.Delete.Force && !recursively(verb, path) {
					continue /* skip this command, process the next */
				}
				if err := v.DeleteTree(path, vault.DeleteOpts{
					Destroy: opt.Delete.Destroy,
					All:     opt.Delete.All,
				}); err != nil && !(vault.IsNotFound(err) && opt.Delete.Force) {
					return err
				}
			} else {
				if err := v.Delete(path, vault.DeleteOpts{
					Destroy: opt.Delete.Destroy,
					All:     opt.Delete.All,
				}); err != nil && !(vault.IsNotFound(err) && opt.Delete.Force) {
					return err
				}
			}
		}
		return nil
	})

	r.Dispatch("undelete", &Help{
		Summary: "Undelete a soft-deleted secret from a V2 backend",
		Usage:   "safe undelete PATH [PATH ...]",
		Type:    DestructiveCommand,
		Description: `
If no version is specified, this attempts to undelete the newest version of the secret
This does not error if the specified version exists but is not deleted
This errors if the secret or version does not exist, of if the particular version has
been irrevocably destroyed. An error also occurs if a key is specified.

-a (--all) undeletes all versions of the given secret.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) < 1 {
			r.ExitWithUsage("undelete")
		}
		v := connect(true)

		for _, path := range args {
			var err error
			if opt.Undelete.All {
				secret, key, version := vault.ParsePath(path)
				if key != "" {
					return fmt.Errorf("Cannot undelete specific key (%s)", path)
				}

				if version > 0 {
					return fmt.Errorf("--all given but path (%s) has version specified", path)
				}

				respVersions, err := v.Versions(secret)
				if err != nil {
					return err
				}

				versions := make([]uint, 0, len(respVersions))
				for _, v := range respVersions {
					versions = append(versions, v.Version)
				}

				err = v.Client().Undelete(path, versions)
			} else {
				err = v.Undelete(path)
			}
			if err != nil {
				return err
			}
		}

		return nil
	})

	r.Dispatch("revert", &Help{
		Summary: "Revert a secret to a previous version",
		Usage:   "safe revert PATH VERSION",
		Type:    DestructiveCommand,
		Description: `
-d (--deleted) will handle deleted versions by undeleting them, reading them, and then
redeleting them.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if len(args) != 2 {
			r.ExitWithUsage("revert")
		}
		v := connect(true)

		secret, key, version := vault.ParsePath(args[0])
		if key != "" {
			return fmt.Errorf("Cannot call revert with path containing key")
		}

		if version > 0 {
			return fmt.Errorf("Cannot call revert with path containing version")
		}

		targetVersion, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("VERSION must be a positive integer")
		}

		if targetVersion == 0 {
			return nil
		}

		//Check what the most recent version is to avoid setting the latest version if unnecessary.
		// This should also catch if the secret is non-existent, or if we're targeting a destroyed,
		// deleted, or non-existent version.
		allVersions, err := v.Versions(args[0])
		if err != nil {
			return err
		}

		destroyedErr := fmt.Errorf("Version %d of secret `%s' is destroyed", targetVersion, secret)
		if targetVersion < uint64(allVersions[0].Version) {
			return destroyedErr
		}

		if targetVersion > uint64(allVersions[len(allVersions)-1].Version) {
			return fmt.Errorf("Version %d of secret `%s' does not exist", targetVersion, secret)
		}

		versionObject := allVersions[targetVersion-uint64(allVersions[0].Version)]
		if versionObject.Destroyed {
			return destroyedErr
		}

		if versionObject.Deleted {
			if !opt.Revert.Deleted {
				return fmt.Errorf("Version %d of secret `%s' is deleted. To force a read, specify --deleted", targetVersion, secret)
			}

			err = v.Undelete(vault.EncodePath(secret, "", targetVersion))
			if err != nil {
				return err
			}
		}

		//If the version to revert to is the current version, do nothing...
		// unless its deleted, then either just undelete it or err, depending on
		// if the -d flag is set
		if targetVersion == uint64(allVersions[len(allVersions)-1].Version) {
			return nil
		}

		toWrite, err := v.Read(vault.EncodePath(secret, "", targetVersion))
		if err != nil {
			return err
		}

		err = v.Write(secret, toWrite)
		if err != nil {
			return err
		}

		//If we got this far and this is set, we must have undeleted a thing.
		// Clean up after ourselves
		if versionObject.Deleted {
			err = v.Delete(vault.EncodePath(secret, "", targetVersion), vault.DeleteOpts{})
			if err != nil {
				return err
			}
		}

		return nil
	})

	r.Dispatch("export", &Help{
		Summary: "Export one or more subtrees for migration / backup purposes",
		Usage:   "safe export [-ad] PATH [PATH ...]",
		Type:    NonDestructiveCommand,
		Description: `
Normally, the export will get only the latest version of each secret, and encode it in a format that is backwards-
compatible with pre-1.0.0 versions of safe (and newer versions).
-a (--all) will encode all versions of each secret. This will cause the export to use the V2 format, which is
incompatible with versions of safe prior to v1.0.0
-d (--deleted) will cause safe to undelete, read, and then redelete deleted secrets in order to encode them in the
backup. Without this, deleted versions will be ignored.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if len(args) < 1 {
			args = append(args, "secret")
		}
		v := connect(true)

		var toExport interface{}

		//Standardize and validate paths
		for i := range args {
			args[i] = vault.Canonicalize(args[i])
			_, key, version := vault.ParsePath(args[i])
			if key != "" {
				return fmt.Errorf("Cannot export path with key (%s)", args[i])
			}

			if version > 0 {
				return fmt.Errorf("Cannot export path with version (%s)", args[i])
			}
		}

		//Deduplicate the input paths
		sort.Slice(args, func(i, j int) bool { return vault.PathLessThan(args[i], args[j]) })
		for i := 0; i < len(args)-1; i++ {
			//No need to get a deeper part of a tree if you're already walking the `((great)*grand)?parent`
			if strings.HasPrefix(strings.Trim(args[i+1], "/"), strings.Trim(args[i], "/")) {
				before := args[:i+1]
				var after []string
				if len(args)-1 != i+1 {
					after = args[i+2:]
				}
				args = append(before, after...)
				i--
			}
		}

		secrets := vault.Secrets{}
		for _, path := range args {
			theseSecrets, err := v.ConstructSecrets(path, vault.TreeOpts{
				FetchKeys:           true,
				FetchAllVersions:    opt.Export.All,
				GetDeletedVersions:  opt.Export.Deleted,
				AllowDeletedSecrets: opt.Export.Deleted,
			})
			if err != nil {
				return err
			}

			secrets = secrets.Merge(theseSecrets)
		}

		var mustV2Export bool
		//Determine if we can get away with a v1 export
		for _, s := range secrets {
			if len(s.Versions) > 1 {
				mustV2Export = true
				break
			}
		}

		v1Export := func() error {
			export := make(map[string]*vault.Secret)
			for _, s := range secrets {
				export[s.Path] = s.Versions[0].Data
			}

			toExport = export
			return nil
		}

		v2Export := func() error {
			export := exportFormat{ExportVersion: 2, Data: map[string]exportSecret{}, RequiresVersioning: map[string]bool{}}

			for _, secret := range secrets {
				if len(secret.Versions) > 1 {
					mount, _ := v.Client().MountPath(secret.Path)
					export.RequiresVersioning[mount] = true
				}

				thisSecret := exportSecret{FirstVersion: secret.Versions[0].Number}
				//We want to omit the `first` key in the json if it's 1
				if thisSecret.FirstVersion == 1 || opt.Export.Shallow {
					thisSecret.FirstVersion = 0
				}

				for _, version := range secret.Versions {
					thisVersion := exportVersion{
						Deleted:   version.State == vault.SecretStateDeleted && opt.Export.Deleted,
						Destroyed: version.State == vault.SecretStateDestroyed || (version.State == vault.SecretStateDeleted && !opt.Export.Deleted),
						Value:     map[string]string{},
					}

					for _, key := range version.Data.Keys() {
						thisVersion.Value[key] = version.Data.Get(key)
					}

					thisSecret.Versions = append(thisSecret.Versions, thisVersion)
				}

				export.Data[secret.Path] = thisSecret

				//Wrap export in array so that older versions of safe don't try to import this improperly.
				toExport = []exportFormat{export}
			}

			return nil
		}

		var err error
		if mustV2Export {
			err = v2Export()
		} else {
			err = v1Export()
		}

		if err != nil {
			return err
		}
		b, err := json.Marshal(&toExport)
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", string(b))

		return nil
	})

	r.Dispatch("import", &Help{
		Summary: "Import name/value pairs into the current Vault",
		Usage:   "safe import <backup/file.json",
		Type:    DestructiveCommand,
		Description: `
-I (--ignore-destroyed) will keep destroyed versions from being replicated in the import by
rting garbage data and then destroying it (which is originally done to preserve version numbering).
-i (--ignore-deleted) will ignore deleted versions from being written during the import.
-s (--shallow) will write only the latest version for each secret.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		b, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		if err != nil {
			return err
		}

		if opt.SkipIfExists {
			fmt.Fprintf(os.Stderr, "@R{!!} @C{--no-clobber} @R{is incompatible with} @C{safe import}\n")
			r.ExitWithUsage("import")
		}

		v := connect(true)

		type importFunc func([]byte) error

		v1Import := func(input []byte) error {
			var data map[string]*vault.Secret
			err := json.Unmarshal(input, &data)
			if err != nil {
				return err
			}
			for path, s := range data {
				err = v.Write(path, s)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "wrote %s\n", path)
			}
			return nil
		}

		v2Import := func(input []byte) error {
			var unmarshalTarget []exportFormat
			err := json.Unmarshal(input, &unmarshalTarget)
			if err != nil {
				return fmt.Errorf("Could not interpret export file: %s", err)
			}

			if len(unmarshalTarget) != 1 {
				return fmt.Errorf("Improperly formatted export file")
			}

			data := unmarshalTarget[0]

			if !opt.Import.Shallow {
				//Verify that the mounts that require versioning actually support it. We
				//can't really detect if v1 mounts exist at this stage unless we assume
				//the token given has mount listing privileges. Not a big deal, because
				//it will become very apparent once we start trying to put secrets in it
				for mount, needsVersioning := range data.RequiresVersioning {
					if needsVersioning {
						mountVersion, err := v.MountVersion(mount)
						if err != nil {
							return fmt.Errorf("Could not determine existing mount version: %s", err)
						}

						if mountVersion != 2 {
							return fmt.Errorf("Export for mount `%s' has secrets with multiple versions, but the mount either\n"+
								"does not exist or does not support versioning", mount)
						}
					}
				}
			}

			//Put the secrets in the places, writing the versions in the correct order and deleting/destroying secrets that
			// need to be deleted/destroyed.
			for path, secret := range data.Data {
				s := vault.SecretEntry{
					Path: path,
				}

				firstVersion := secret.FirstVersion
				if firstVersion == 0 {
					firstVersion = 1
				}

				if opt.Import.Shallow {
					secret.Versions = secret.Versions[len(secret.Versions)-1:]
				}
				for i := range secret.Versions {
					state := vault.SecretStateAlive
					if secret.Versions[i].Destroyed {
						if opt.Import.IgnoreDestroyed {
							continue
						}
						state = vault.SecretStateDestroyed
					} else if secret.Versions[i].Deleted {
						if opt.Import.IgnoreDeleted {
							continue
						}
						state = vault.SecretStateDeleted
					}
					data := vault.NewSecret()
					for k, v := range secret.Versions[i].Value {
						data.Set(k, v, false)
					}
					s.Versions = append(s.Versions, vault.SecretVersion{
						Number: firstVersion + uint(i),
						State:  state,
						Data:   data,
					})
				}

				err := s.Copy(v, s.Path, vault.TreeCopyOpts{
					Clear: true,
					Pad:   !(opt.Import.IgnoreDestroyed || opt.Import.Shallow),
				})
				if err != nil {
					return err
				}
			}

			return nil
		}

		var fn importFunc
		//determine which version of the export format this is
		var typeTest interface{}
		json.Unmarshal(b, &typeTest)
		switch v := typeTest.(type) {
		case map[string]interface{}:
			fn = v1Import
		case []interface{}:
			if len(v) == 1 {
				if meta, isMap := (v[0]).(map[string]interface{}); isMap {
					version, isFloat64 := meta["export_version"].(float64)
					if isFloat64 && version == 2 {
						fn = v2Import
					}
				}
			}
		}

		if fn == nil {
			return fmt.Errorf("Unknown export file format - aborting")
		}

		return fn(b)
	})

	r.Dispatch("move", &Help{
		Summary: "Move a secret from one path to another",
		Usage:   "safe move [-rfd] OLD-PATH NEW-PATH",
		Type:    DestructiveCommand,
		Description: `
Specifying the --deep (-d) flag will cause versions to be grabbed from the source
and overwrite all versions of the secret at the destination.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		if len(args) != 2 {
			r.ExitWithUsage("move")
		}

		v := connect(true)
		if vault.PathHasKey(args[0]) || vault.PathHasKey(args[1]) {
			if opt.Move.Deep {
				return fmt.Errorf("Cannot deep copy a specific key")
			}

			if !vault.PathHasKey(args[0]) && vault.PathHasKey(args[1]) {
				return fmt.Errorf("Cannot move from entire secret into specific key")
			}
		}

		if vault.PathHasVersion(args[1]) {
			return fmt.Errorf("Cannot move to a specific destination version")
		}

		//Don't try to recurse if operating on a key
		// args[0] is the source path. args[1] is the destination path.
		if opt.Move.Recurse && !(vault.PathHasKey(args[0]) || vault.PathHasKey(args[1])) {
			if !opt.Move.Force && !recursively("move", args...) {
				return nil /* skip this command, process the next */
			}
			err := v.MoveCopyTree(args[0], args[1], v.Move, vault.MoveCopyOpts{
				SkipIfExists: opt.SkipIfExists, Quiet: opt.Quiet, Deep: opt.Move.Deep, DeletedVersions: opt.Move.Deep,
			})
			if err != nil && !(vault.IsNotFound(err) && opt.Move.Force) {
				return err
			}
		} else {
			err := v.Move(args[0], args[1], vault.MoveCopyOpts{
				SkipIfExists: opt.SkipIfExists, Quiet: opt.Quiet, Deep: opt.Move.Deep, DeletedVersions: opt.Move.Deep,
			})
			if err != nil && !(vault.IsNotFound(err) && opt.Move.Force) {
				return err
			}
		}
		return nil
	})

	r.Dispatch("copy", &Help{
		Summary: "Copy a secret from one path to another",
		Usage:   "safe copy [-rfd] OLD-PATH NEW-PATH",
		Type:    DestructiveCommand,
		Description: `
Specifying the --deep (-d) flag will cause all living versions to be grabbed from the source
and overwrite all versions of the secret at the destination.
`}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) != 2 {
			r.ExitWithUsage("copy")
		}
		v := connect(true)

		if vault.PathHasKey(args[0]) || vault.PathHasKey(args[1]) {
			if opt.Copy.Deep {
				return fmt.Errorf("Cannot deep copy a specific key")
			}

			if !vault.PathHasKey(args[0]) && vault.PathHasKey(args[1]) {
				return fmt.Errorf("Cannot move from entire secret into specific key")
			}
		}

		if vault.PathHasVersion(args[1]) {
			return fmt.Errorf("Cannot copy to a specific destination version")
		}

		if opt.Copy.Recurse && vault.PathHasVersion(args[0]) {
			return fmt.Errorf("Cannot recursively copy a path with specific version")
		}

		//Don't try to recurse if operating on a key
		// args[0] is the source path. args[1] is the destination path.
		if opt.Copy.Recurse && !(vault.PathHasKey(args[0]) || vault.PathHasKey(args[1])) {
			if !opt.Copy.Force && !recursively("copy", args...) {
				return nil /* skip this command, process the next */
			}
			err := v.MoveCopyTree(args[0], args[1], v.Copy, vault.MoveCopyOpts{
				SkipIfExists:    opt.SkipIfExists,
				Quiet:           opt.Quiet,
				Deep:            opt.Copy.Deep,
				DeletedVersions: opt.Copy.Deep,
			})
			if err != nil && !(vault.IsNotFound(err) && opt.Copy.Force) {
				return err
			}
		} else {
			err := v.Copy(args[0], args[1], vault.MoveCopyOpts{
				SkipIfExists:    opt.SkipIfExists,
				Quiet:           opt.Quiet,
				Deep:            opt.Copy.Deep,
				DeletedVersions: opt.Copy.Deep,
			})
			if err != nil && !(vault.IsNotFound(err) && opt.Copy.Force) {
				return err
			}
		}
		return nil
	})

	r.Dispatch("gen", &Help{
		Summary: "Generate a random password",
		Usage:   "safe gen [-l <length>] [-p] PATH:KEY [PATH:KEY ...]",
		Type:    DestructiveCommand,
		Description: `
LENGTH defaults to 64 characters.

The following options are recognized:

  -l, --length  Specify the length of the random string to generate
	-p, --policy  Specify a regex character grouping for limiting characters used
	              to generate the password (e.g --policy a-z0-9)
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) == 0 {
			r.ExitWithUsage("gen")
		}

		length := 64

		if opt.Gen.Length != 0 {
			length = opt.Gen.Length
		} else if u, err := strconv.ParseUint(args[0], 10, 16); err == nil {
			length = int(u)
			args = args[1:]
		}

		v := connect(true)

		for len(args) > 0 {
			var path, key string
			if vault.PathHasKey(args[0]) {
				path, key, _ = vault.ParsePath(args[0])
				args = args[1:]
			} else {
				if len(args) < 2 {
					r.ExitWithUsage("gen")
				}
				path, key = args[0], args[1]
				//If the key looks like a full path with a :key at the end, then the user
				// probably botched the args
				if vault.PathHasKey(key) {
					return fmt.Errorf("For secret `%s` and key `%s`: key cannot contain a key", path, key)
				}
				args = args[2:]
			}
			s, err := v.Read(path)
			if err != nil && !vault.IsNotFound(err) {
				return err
			}
			exists := (err == nil)
			if opt.SkipIfExists && exists && s.Has(key) {
				if !opt.Quiet {
					fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to update} @C{%s:%s} @R{as it is already present in Vault}\n", path, key)
				}
				continue
			}
			err = s.Password(key, length, opt.Gen.Policy, opt.SkipIfExists)
			if err != nil {
				return err
			}

			if err = v.Write(path, s); err != nil {
				return err
			}
		}
		return nil
	})

	r.Dispatch("uuid", &Help{
		Summary:     "Generate a new UUIDv4",
		Usage:       "safe uuid PATH[:KEY]",
		Type:        DestructiveCommand,
		Description: ``,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) != 1 {
			r.ExitWithUsage("uuid")
		}

		u := uuid.NewRandom()

		stringuuid := u.String()

		v := connect(true)

		var path, key string
		if vault.PathHasKey(args[0]) {
			path, key, _ = vault.ParsePath(args[0])

		} else {
			path, key = args[0], "uuid"
			//If the key looks like a full path with a :key at the end, then the user
			//probably botched the args
			if vault.PathHasKey(key) {
				return fmt.Errorf("For secret `%s` and key `%s`: key cannot contain a key", path, key)
			}

		}
		s, err := v.Read(path)
		if err != nil && !vault.IsNotFound(err) {
			return err
		}
		exists := (err == nil)
		if opt.SkipIfExists && exists && s.Has(key) {
			if !opt.Quiet {
				fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to update} @C{%s:%s} @R{as it is already present in Vault}\n", path, key)
			}
			return err
		}
		err = s.Set(key, stringuuid, opt.SkipIfExists)
		if err != nil {
			return err
		}

		if err = v.Write(path, s); err != nil {
			return err
		}

		return nil
	})

	r.Dispatch("option", &Help{
		Summary: "View or edit global safe CLI options",
		Usage:   "safe option [optionname=value]",
		Type:    AdministrativeCommand,
		Description: `
Currently available options are:

@G{manage_vault_token}    If set to true, then when logging in or switching targets,
                      the '.vault-token' file in your $HOME directory that the Vault CLI uses will be 
                      updated.
`,
	}, func(command string, args ...string) error {
		cfg := rc.Apply(opt.UseTarget)

		optLookup := []struct {
			opt string
			val *bool
		}{
			{"manage_vault_token", &cfg.Options.ManageVaultToken},
		}

		if len(args) == 0 {
			table := table{}
			for _, entry := range optLookup {
				value := "@R{false}"
				if *entry.val {
					value = "@G{true}"
				}
				table.addRow(entry.opt, ansi.Sprintf(value))
			}

			table.print()
			return nil
		}

		for _, arg := range args {
			argSplit := strings.Split(arg, "=")
			if len(argSplit) != 2 {
				return fmt.Errorf("Option arg syntax: option=value")
			}

			parseTrueFalse := func(s string) (bool, error) {
				switch s {
				case "true", "on", "yes":
					return true, nil
				case "false", "off", "no":
					return false, nil
				}

				return false, fmt.Errorf("value must be one of true|on|yes|false|off|no")
			}

			optionKey := strings.ReplaceAll(argSplit[0], "-", "_")
			optionVal, err := parseTrueFalse(argSplit[1])
			if err != nil {
				return err
			}

			found := false
			for _, opt := range optLookup {
				if opt.opt == optionKey {
					found = true
					*opt.val = optionVal
					ansi.Printf("updated @G{%s}\n", opt.opt)
					break
				}
			}

			if !found {
				return fmt.Errorf("unknown option: %s", argSplit[0])
			}
		}

		return cfg.Write()
	})

	r.Dispatch("ssh", &Help{
		Summary: "Generate one or more new SSH RSA keypair(s)",
		Usage:   "safe ssh [NBITS] PATH [PATH ...]",
		Type:    DestructiveCommand,
		Description: `
For each PATH given, a new SSH RSA public/private keypair will be generated,
with a key strength of NBITS (which defaults to 2048).  The private keys will
be stored under the 'private' name, as a PEM-encoded RSA private key, and the
public key, formatted for use in an SSH authorized_keys file, under 'public'.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		bits := 2048
		if len(args) > 0 {
			if u, err := strconv.ParseUint(args[0], 10, 16); err == nil {
				bits = int(u)
				args = args[1:]
			}
		}

		if len(args) < 1 {
			r.ExitWithUsage("ssh")
		}

		v := connect(true)
		for _, path := range args {
			s, err := v.Read(path)
			if err != nil && !vault.IsNotFound(err) {
				return err
			}
			exists := (err == nil)
			if opt.SkipIfExists && exists && (s.Has("private") || s.Has("public") || s.Has("fingerprint")) {
				if !opt.Quiet {
					fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to generate an SSH key at} @C{%s} @R{as it is already present in Vault}\n", path)
				}
				continue
			}
			if err = s.SSHKey(bits, opt.SkipIfExists); err != nil {
				return err
			}
			if err = v.Write(path, s); err != nil {
				return err
			}
		}
		return nil
	})

	r.Dispatch("rsa", &Help{
		Summary: "Generate a new RSA keypair",
		Usage:   "safe rsa [NBITS] PATH [PATH ...]",
		Type:    DestructiveCommand,
		Description: `
For each PATH given, a new RSA public/private keypair will be generated with a,
key strength of NBITS (which defaults to 2048).  The private keys will be stored
under the 'private' name, and the public key under the 'public' name.  Both will
be PEM-encoded.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		bits := 2048
		if len(args) > 0 {
			if u, err := strconv.ParseUint(args[0], 10, 16); err == nil {
				bits = int(u)
				args = args[1:]
			}
		}

		if len(args) < 1 {
			r.ExitWithUsage("rsa")
		}

		v := connect(true)
		for _, path := range args {
			s, err := v.Read(path)
			if err != nil && !vault.IsNotFound(err) {
				return err
			}
			exists := (err == nil)
			if opt.SkipIfExists && exists && (s.Has("private") || s.Has("public")) {
				if !opt.Quiet {
					fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to generate an RSA key at} @C{%s} @R{as it is already present in Vault}\n", path)
				}
				continue
			}
			if err = s.RSAKey(bits, opt.SkipIfExists); err != nil {
				return err
			}
			if err = v.Write(path, s); err != nil {
				return err
			}
		}
		return nil
	})

	r.Dispatch("dhparam", &Help{
		Summary: "Generate Diffie-Helman key exchange parameters",
		Usage:   "safe dhparam [NBITS] PATH",
		Type:    DestructiveCommand,
		Description: `
NBITS defaults to 2048.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)
		bits := 2048

		if len(args) > 0 {
			if u, err := strconv.ParseUint(args[0], 10, 16); err == nil {
				bits = int(u)
				args = args[1:]
			}
		}

		if len(args) < 1 {
			r.ExitWithUsage("dhparam")
		}

		path := args[0]
		v := connect(true)
		s, err := v.Read(path)
		if err != nil && !vault.IsNotFound(err) {
			return err
		}
		exists := (err == nil)
		if opt.SkipIfExists && exists && s.Has("dhparam-pem") {
			if !opt.Quiet {
				fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to generate a Diffie-Hellman key exchange parameter set at} @C{%s} @R{as it is already present in Vault}\n", path)
			}
			return nil
		}
		if err = s.DHParam(bits, opt.SkipIfExists); err != nil {
			return err
		}
		return v.Write(path, s)
	})

	r.Dispatch("prompt", &Help{
		Summary: "Print a prompt (useful for scripting safe command sets)",
		Usage:   "safe echo Your Message Here:",
		Type:    NonDestructiveCommand,
	}, func(command string, args ...string) error {
		// --no-clobber is ignored here, because there's no context of what you're
		// about to be writing after a prompt, so not sure if we should or shouldn't prompt
		// if you need to write something and prompt, but only if it isnt already present
		// in vault, see the `ask` subcommand
		fmt.Fprintf(os.Stderr, "%s\n", strings.Join(args, " "))
		return nil
	})

	r.Dispatch("vault", &Help{
		Summary: "Run arbitrary Vault CLI commands against the current target",
		Usage:   "safe vault ...",
		Type:    DestructiveCommand,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if opt.SkipIfExists {
			fmt.Fprintf(os.Stderr, "@C{--no-clobber} @Y{specified, but is ignored for} @C{safe vault}\n")
		}

		proxy, err := vault.NewProxyRouter()
		if err != nil {
			return err
		}

		cmd := exec.Command("vault", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		//If the command is vault status, we don't want to expose the VAULT_NAMESPACE envvar
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") {
				if arg == "status" {
					os.Unsetenv("VAULT_NAMESPACE")
				}
				break
			}
		}
		cmd.Env = os.Environ()

		//Make sure we don't accidentally specify a http_proxy and a HTTP_PROXY
		for i := range cmd.Env {
			parts := strings.Split(cmd.Env[i], "=")
			if len(parts) < 2 {
				continue
			}
			if parts[0] == "http_proxy" || parts[0] == "https_proxy" || parts[0] == "no_proxy" {
				cmd.Env[i] = strings.ToUpper(parts[0]) + "=" + strings.Join(parts[1:], "=")
			}
		}

		if proxy.ProxyConf.HTTPProxy != "" {
			cmd.Env = append(cmd.Env, "HTTP_PROXY="+proxy.ProxyConf.HTTPProxy)
		}

		if proxy.ProxyConf.HTTPSProxy != "" {
			cmd.Env = append(cmd.Env, "HTTPS_PROXY="+proxy.ProxyConf.HTTPSProxy)
		}

		if proxy.ProxyConf.NoProxy != "" {
			cmd.Env = append(cmd.Env, "NO_PROXY="+proxy.ProxyConf.NoProxy)
		}

		err = cmd.Run()
		if err != nil {
			return err
		}
		return nil
	})

	r.Dispatch("rekey", &Help{
		Summary: "Re-key your Vault with new unseal keys",
		Usage:   "safe rekey [--gpg email@address ...] [--keys #] [--threshold #]",
		Type:    DestructiveCommand,
		Description: `
Rekeys Vault with new unseal keys. This will require a quorum
of existing unseal keys to accomplish. This command can be used
to change the nubmer of unseal keys being generated via --keys,
as well as the number of keys required to unseal the Vault via
--threshold.

If --gpg flags are provided, they will be used to look up in the
local GPG keyring public keys to give Vault for encrypting the new
unseal keys (one pubkey per unseal key). Output will have the
encrypted unseal keys, matched up with the email address associated
with the public key that it was encrypted with. Additionally, a
backup of the encrypted unseal keys is located at sys/rekey/backup
in Vault.

If no --gpg flags are provided, the output will include the raw
unseal keys, and should be treated accordingly.

By default, the new seal keys will also be stored in the Vault itself,
unless you specify the --no-persist flag.  They will be written to
secret/vault/seal/keys, as key1, key2, ... keyN.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		unsealKeys := 5 // default to 5
		var gpgKeys []string
		if len(opt.Rekey.GPG) > 0 {
			unsealKeys = len(opt.Rekey.GPG)
			for _, email := range opt.Rekey.GPG {
				output, err := exec.Command("gpg", "--export", email).Output()
				if err != nil {
					return fmt.Errorf("Failed to retrieve GPG key for %s from local keyring: %s", email, err.Error())
				}

				// gpg --export returns 0, with no stdout if the key wasn't found, so handle that
				if output == nil || len(output) == 0 {
					return fmt.Errorf("No GPG key found for %s in the local keyring", email)
				}
				gpgKeys = append(gpgKeys, base64.StdEncoding.EncodeToString(output))
			}
		}

		// if specified, --unseal-keys takes priority, then the number of --gpg-keys, and a default of 5
		if opt.Rekey.NKeys != 0 {
			unsealKeys = opt.Rekey.NKeys
		}
		if len(opt.Rekey.GPG) > 0 && unsealKeys != len(opt.Rekey.GPG) {
			return fmt.Errorf("Both --gpg and --keys were specified, and their counts did not match.")
		}

		// if --threshold isn't specified, use a default (unless default is > the number of keys
		if opt.Rekey.Threshold == 0 {
			opt.Rekey.Threshold = 3
			if opt.Rekey.Threshold > unsealKeys {
				opt.Rekey.Threshold = unsealKeys
			}
		}
		if opt.Rekey.Threshold > unsealKeys {
			return fmt.Errorf("You specified only %d unseal keys, but are requiring %d keys to unseal vault. This is bad.", unsealKeys, opt.Rekey.Threshold)
		}
		if opt.Rekey.Threshold < 2 && unsealKeys > 1 {
			return fmt.Errorf("When specifying more than 1 unseal key, you must also have more than one key required to unseal.")
		}

		v := connect(true)
		keys, err := v.ReKey(unsealKeys, opt.Rekey.Threshold, gpgKeys)
		if err != nil {
			return err
		}

		if opt.Rekey.Persist {
			v.SaveSealKeys(keys)
		}

		fmt.Printf("@G{Your Vault has been re-keyed.} Please take note of your new unseal keys and @R{store them safely!}\n")
		for i, key := range keys {
			if len(opt.Rekey.GPG) == len(keys) {
				fmt.Printf("Unseal key for @c{%s}:\n@y{%s}\n", opt.Rekey.GPG[i], key)
			} else {
				fmt.Printf("Unseal key %d: @y{%s}\n", i+1, key)
			}
		}

		return nil
	})

	r.Dispatch("fmt", &Help{
		Summary: "Reformat an existing name/value pair, into a new name",
		Usage:   "safe fmt FORMAT PATH OLD-NAME NEW-NAME",
		Type:    DestructiveCommand,
		Description: `
Take the value stored at PATH/OLD-NAME, format it a different way, and
then save it at PATH/NEW-NAME.  This can be useful for generating a new
password (via 'safe gen') and then crypt'ing it for use in /etc/shadow,
using the 'crypt-sha512' format.

Supported formats:

    base64          Base64 encodes the value
    bcrypt          Salt and hash the value, using bcrypt (Blowfish, in crypt format).
    crypt-md5       Salt and hash the value, using MD5, in crypt format (legacy).
    crypt-sha256    Salt and hash the value, using SHA-256, in crypt format.
    crypt-sha512    Salt and hash the value, using SHA-512, in crypt format.

`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) != 4 {
			r.ExitWithUsage("fmt")
		}

		fmtType := args[0]
		path := args[1]
		oldKey := args[2]
		newKey := args[3]

		v := connect(true)
		s, err := v.Read(path)
		if err != nil {
			return err
		}
		if opt.SkipIfExists && s.Has(newKey) {
			if !opt.Quiet {
				fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to reformat} @C{%s:%s} @R{to} @C{%s} @R{as it is already present in Vault}\n", path, oldKey, newKey)
			}
			return nil
		}
		if err = s.Format(oldKey, newKey, fmtType, opt.SkipIfExists); err != nil {
			if vault.IsNotFound(err) {
				return fmt.Errorf("%s:%s does not exist, cannot create %s encoded copy at %s:%s", path, oldKey, fmtType, path, newKey)
			}
			return fmt.Errorf("Error encoding %s:%s as %s: %s", path, oldKey, fmtType, err)
		}

		return v.Write(path, s)
	})

	r.Dispatch("curl", &Help{
		Summary: "Issue arbitrary HTTP requests to the current Vault (for diagnostics)",
		Usage:   "safe curl [OPTIONS] METHOD REL-URI [DATA]",
		Type:    DestructiveCommand,
		Description: `
This is a debugging and diagnostics tool.  You should not need to use
'safe curl' for normal operation or interaction with a Vault.

The following OPTIONS are recognized:

  --data-only         Show only the response body, hiding the response headers.

METHOD must be one of GET, LIST, POST, or PUT.

REL-URI is the relative URI (the path component, starting with the first
forward slash) of the resource you wish to access.

DATA should be a JSON string, since almost all of the Vault API handlers
deal exclusively in JSON payloads.  GET requests should not have DATA.
Query string parameters should be appended to REL-URI, instead of being
sent as DATA.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		var (
			url, method string
			data        []byte
		)

		method = "GET"
		if len(args) < 1 {
			r.ExitWithUsage("curl")
		} else if len(args) == 1 {
			url = args[0]
		} else {
			method = strings.ToUpper(args[0])
			url = args[1]
			data = []byte(strings.Join(args[2:], " "))
		}

		v := connect(true)
		res, err := v.Curl(method, url, data)
		if err != nil {
			return err
		}

		if opt.Curl.DataOnly {
			b, err := ioutil.ReadAll(res.Body)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "%s\n", string(b))

		} else {
			r, _ := httputil.DumpResponse(res, true)
			fmt.Fprintf(os.Stdout, "%s\n", r)
		}
		return nil
	})

	r.Dispatch("x509", &Help{
		Summary: "Issue / Revoke X.509 Certificates and Certificate Authorities",
		Usage:   "safe x509 <command> [OPTIONS]",
		Type:    HiddenCommand,
		Description: `
x509 provides a handful of sub-commands for issuing, signing and revoking
SSL/TLS X.509 Certificates.  It does not utilize the pki Vault backend;
instead, all certificates and RSA keys are generated by the CLI itself,
and stored wherever you tell it to.

Here are the supported commands:

  @G{x509 issue} [OPTIONS] path/to/store/cert/in

    Issues a new X.509 certificate, which can be either self-signed,
    or signed by another CA certificate, elsewhere in the Vault.
    You can control the subject name, alternate names (DNS, email and
    IP addresses), Key Usage, Extended Key Usage, and TTL/expiry.


  @G{x509 revoke} [OPTIONS] path/to/cert

    Revokes an X.509 certificate that was issued by one of our CAs.


  @G{x509 crl} [OPTIONS] path/to/ca

    Manages a certificate revocation list, primarily to renew it
    (resigning it for freshness / liveness).


  @G{x509 validate} [OPTIONS] path/to/cert

    Validate a certificate in the Vault, checking to make sure that
    its private and public keys match, checking CA signatories,
    expiration, name applicability, etc.

  @G{x509 show} path/to/cert [path/to/other/cert ...]

    Print out a human-readable description of the certificate,
    including its subject name, issuer (CA), expiration and lifetime,
    and what domains, email addresses, and IP addresses it represents.

  @G{x509 reissue} [OPTIONS] path/to/certificate

    Regenerate the certificate and key at the given path.

  @G{x509 renew} [OPTIONS] path/to/certificate

    Renew the certificate at the given path
`,
	}, func(command string, args ...string) error {
		r.Help(os.Stdout, "x509")
		return nil
	})

	r.Dispatch("x509 validate", &Help{
		Summary: "Validate an X.509 Certificate / Private Key",
		Usage:   "safe x509 validate [OPTIONS} path/to/certificate/or/ca",
		Type:    NonDestructiveCommand,
		Description: `
Certificate validation can be checked in many ways, and this utility
provides most of them, including:

  - Certificate matches private key (default)
  - Certificate was signed by a given CA (--signed-by x)
  - Certificate is not revoked by its CA (--not-revoked)
  - Certificate is not expired (--not-expired)
  - Certificate is valid for a given name / IP / email address (--for)
  - RSA Private Key strength,in bits (--bits)

If any of the selected validations fails, safe will immediately exit
with a non-zero exit code to signal failure.  This can be used in scripts
to check certificates and alter behavior depending on their validity.

If the validations pass, safe will continue on to execute subsequent
sub-commands.

For revocation and expiry checks there are both positive validations (i.e.
this certificate *is* expired) and negative validations (not revoked).
This approach allows you to validate that the certificate you revoked is
actually revoked, while still validating that the certificate and key match,
CA signing constraints, etc.

The following options are recognized:

  -A, --ca            Check that this is a Certificate Authority, with the
                      ability to sign other certifictes.

  -i, --signed-by X   The path to the CA that signed this certificate.
                      safe will check that the CA is the one who signed
                      the certificate, and that the signature is valid.

  -R, --not-revoked   Verify that the certificate has not been revoked
                      by its signing CA.  This makes little sense with
                      self-signed certificates.  Requires the --signed-by
                      option to be specified.

  -r, --revoked       The opposite of --not-revoked; Verify that the CA
                      has revoked the certificate.  Requires --signed-by.

  -E, --not-expired   Check that the certificate is still valid, according
                      to its NotBefore / NotAfter values.

  -e, --expired       Check that the certificate is either not yet valid,
                      or is no longer valid.

  -n, --for N         Check a name / IP / email address against the CN
                      and subject alternate names (of the correct type),
                      to see if the certificate was issued for this name.
                      This can be specified multiple times, in which case
                      all checks must pass for safe to exit zero.

  -b, --bits N        Check that the RSA private key for this certificate
                      has the specified key size (in bits).  This can be
                      specified more than once, in which case any match
                      will pass validation.
`,
	}, func(command string, args ...string) error {
		if len(args) < 1 {
			r.ExitWithUsage("x509 validate")
		}
		if opt.X509.Validate.SignedBy == "" && opt.X509.Validate.Revoked {
			r.ExitWithUsage("x509 validate")
		}
		if opt.X509.Validate.SignedBy == "" && opt.X509.Validate.NotRevoked {
			r.ExitWithUsage("x509 validate")
		}

		rc.Apply(opt.UseTarget)
		v := connect(true)

		var ca *vault.X509
		if opt.X509.Validate.SignedBy != "" {
			s, err := v.Read(opt.X509.Validate.SignedBy)
			if err != nil {
				return err
			}
			ca, err = s.X509(true)
			if err != nil {
				return err
			}
		}

		for _, path := range args {
			s, err := v.Read(path)
			if err != nil {
				return err
			}
			cert, err := s.X509(true)
			if err != nil {
				return err
			}

			if err = cert.Validate(); err != nil {
				return fmt.Errorf("%s failed validation: %s", path, err)
			}

			if opt.X509.Validate.Bits != nil {
				if err = cert.CheckStrength(opt.X509.Validate.Bits...); err != nil {
					return fmt.Errorf("%s failed strength requirement: %s", path, err)
				}
			}

			if opt.X509.Validate.CA && !cert.IsCA() {
				return fmt.Errorf("%s is not a certificate authority", path)
			}

			if opt.X509.Validate.Revoked && !ca.HasRevoked(cert) {
				return fmt.Errorf("%s has not been revoked by %s", path, opt.X509.Validate.SignedBy)
			}
			if opt.X509.Validate.NotRevoked && ca.HasRevoked(cert) {
				return fmt.Errorf("%s has been revoked by %s", path, opt.X509.Validate.SignedBy)
			}

			if opt.X509.Validate.Expired && !cert.Expired() {
				return fmt.Errorf("%s has not yet expired", path)
			}
			if opt.X509.Validate.NotExpired && cert.Expired() {
				return fmt.Errorf("%s has expired", path)
			}

			if _, err = cert.ValidFor(opt.X509.Validate.Name...); err != nil {
				return err
			}

			if cert.IsCA() {
				if cert.Serial == nil {
					return fmt.Errorf("%s is missing its serial number tracker", path)
				}
				if cert.CRL == nil {
					return fmt.Errorf("%s is missing its certificate revocation list", path)
				}
			}

			if ca != nil { //If --signed-by was specified...
				err = cert.Certificate.CheckSignatureFrom(ca.Certificate)

				if err != nil {
					return fmt.Errorf("%s was not signed by %s", path, opt.X509.Validate.SignedBy)
				}
			}

			fmt.Printf("@G{%s} checks out.\n", path)
		}

		return nil
	})

	r.Dispatch("x509 issue", &Help{
		Summary: "Issue X.509 Certificates and Certificate Authorities",
		Usage:   "safe x509 issue [OPTIONS] --name cn.example.com path/to/certificate",
		Type:    DestructiveCommand,
		Description: `
Issue a new X.509 Certificate

The following options are recognized:

  -A, --ca            This certificate is a CA, and can
                      sign other certificates.

  -s, --subject       The subject name for this certificate.
                      i.e. /cn=www.example.com/c=us/st=ny...
                      If not specified, the first '--name'
                      will be used as a lone CN=...

  -i, --signed-by     Path in the Vault where the CA certificate
                      (and signing key) can be found.
                      Without this option, 'x509 issue' creates
                      self-signed certificates.

  -n, --name          Subject Alternate Name(s) for this
                      certificate.  These can be domain names,
                      IP addresses or email address -- safe will
                      figure out how to properly encode them.
                      Can (and probably should) be specified
                      more than once.

  -b, --bits N        RSA key strength, in bits.  The only valid
                      arguments are 1024 (highly discouraged),
                      2048 and 4096.  Defaults to 4096.

  -t, --ttl           How long the new certificate will be valid
                      for.  Specified in units h (hours), m (months)
                      d (days) or y (years).  1m = 30d and 1y = 365d
                      Defaults to 10y for CA certificates and 2y otherwise.

  -u, --key-usage     An x509 key usage or extended key usage. Can be specified
                      once for each desired usage. Valid key usage values are:
                      'digital_signature', 'non_repudiation', 'key_encipherment',
                      'data_encipherment', 'key_agreement', 'key_cert_sign',
                      'crl_sign', 'encipher_only', or 'decipher_only'. Valid
                      extended key usages are 'client_auth', 'server_auth', 'code_signing',
                      'email_protection', or 'timestamping'. The default extended
                      key usages are 'server_auth' and 'client_auth'. CA certs
                      will additionally have the default key usages of key_cert_sign
                      and crl_sign. Specifying any key usages manually will override
                      all of these defaults. To specify no key usages, add 'no' as the
                      only key usage.

  -l, --sig-algorithm The algorithm that the certificate will be signed
                      with. Valid values are md5-rsa, sha1-rsa, sha256-rsa
                      sha384-rsa, sha512-rsa, sha256-rsapss, sha384-rsapss,
                      sha512-rsapss, dsa-sha1, dsa-sha256, ecdsa-sha1,
                      ecdsa-sha256, ecdsa-sha384, and ecdsa-sha512. Defaults
                      to sha512-rsa.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		var ca *vault.X509

		if len(args) != 1 || len(opt.X509.Issue.Name) == 0 {
			r.ExitWithUsage("x509 issue")
		}

		if opt.X509.Issue.Subject == "" {
			opt.X509.Issue.Subject = fmt.Sprintf("CN=%s", opt.X509.Issue.Name[0])
		}

		v := connect(true)
		if opt.SkipIfExists {
			if _, err := v.Read(args[0]); err == nil {
				if !opt.Quiet {
					fmt.Fprintf(os.Stderr, "@R{Cowardly refusing to create a new certificate in} @C{%s} @R{as it is already present in Vault}\n", args[0])
				}
				return nil
			} else if err != nil && !vault.IsNotFound(err) {
				return err
			}
		}

		if opt.X509.Issue.SignedBy != "" {
			secret, err := v.Read(opt.X509.Issue.SignedBy)
			if err != nil {
				return err
			}

			ca, err = secret.X509(true)
			if err != nil {
				return err
			}
		}

		if len(opt.X509.Issue.KeyUsage) == 0 {
			opt.X509.Issue.KeyUsage = append(opt.X509.Issue.KeyUsage, "server_auth", "client_auth")
			if opt.X509.Issue.CA {
				opt.X509.Issue.KeyUsage = append(opt.X509.Issue.KeyUsage, "key_cert_sign", "crl_sign")
			}
		}

		cert, err := vault.NewCertificate(opt.X509.Issue.Subject,
			uniq(opt.X509.Issue.Name), opt.X509.Issue.KeyUsage,
			opt.X509.Issue.SigAlgorithm, opt.X509.Issue.Bits)
		if err != nil {
			return err
		}

		if opt.X509.Issue.CA {
			cert.MakeCA()
		}

		if opt.X509.Issue.TTL == "" {
			opt.X509.Issue.TTL = "2y"
			if opt.X509.Issue.CA {
				opt.X509.Issue.TTL = "10y"
			}
		}
		ttl, err := duration(opt.X509.Issue.TTL)
		if err != nil {
			return err
		}
		if ca == nil {
			if err := cert.Sign(cert, ttl); err != nil {
				return err
			}
		} else {
			if err := ca.Sign(cert, ttl); err != nil {
				return err
			}

			err = ca.SaveTo(v, opt.X509.Issue.SignedBy, opt.SkipIfExists)
			if err != nil {
				return err
			}
		}

		err = cert.SaveTo(v, args[0], opt.SkipIfExists)
		if err != nil {
			return err
		}

		return nil
	})

	r.Dispatch("x509 reissue", &Help{
		Summary: "Reissue X.509 Certificates and Certificate Authorities",
		Usage:   "safe x509 reissue [OPTIONS] path/to/certificate",
		Type:    DestructiveCommand,
		Description: `
Reissues an X.509 Certificate with a new key.

The following options are recognized:

  -s, --subject       The subject name for this certificate.
                      i.e. /cn=www.example.com/c=us/st=ny...
                      Unlike in x509 issue, the subject will not automatically
                      take the first SAN - if you want to update it, you will
											need to specify this flag explicitly. Use caution when
                      changing the subject of a CA cert, as it will
                      invalidate the chain of trust between the CA and
                      certificates it has signed for many client implementations.

  -n, --name          Subject Alternate Name(s) for this
                      certificate.  These can be domain names,
                      IP addresses or email address -- safe will
                      figure out how to properly encode them.
                      Can (and probably should) be specified
											more than once. This flag will not append additional SANs,
											it will act as an exhaustive list in the same way that
                      it would for a new issue command.

  -b, --bits  N       RSA key strength, in bits.  The only valid
                      arguments are 1024 (highly discouraged),
                      2048 and 4096.  Defaults to the last value used
                      to (re)issue the certificate.

  -i, --signed-by     Path in the Vault where the CA certificate
                      (and signing key) can be found.  If this is not
                      provided, a sibling secret named 'ca' will used
                      if it exists. This should be the same CA that
                      originally signed the certificate, but does not
                      have to be.

  -t, --ttl           How long the new certificate will be valid
                      for.  Specified in units h (hours), m (months)
                      d (days) or y (years).  1m = 30d and 1y = 365d
                      Defaults to the last TTL used to issue or renew
                      the certificate.

  -u, --key-usage     An x509 key usage or extended key usage. Can be specified
                      once for each desired usage. Valid key usage values are:
                      'digital_signature', 'non_repudiation', 'key_encipherment',
                      'data_encipherment', 'key_agreement', 'key_cert_sign',
                      'crl_sign', 'encipher_only', or 'decipher_only'. Valid
                      extended key usages are 'client_auth', 'server_auth', 'code_signing',
                      'email_protection', or 'timestamping'. The default extended
                      key usages are 'server_auth' and 'client_auth'. CA certs
                      will additionally have the default key usages of key_cert_sign
                      and crl_sign. Specifying any key usages manually will override
                      all of these defaults. To specify no key usages, add 'no' as the
											only key usage.

  -l, --sig-algorithm The algorithm that the certificate will be signed
                      with. Valid values are md5-rsa, sha1-rsa, sha256-rsa
                      sha384-rsa, sha512-rsa, sha256-rsapss, sha384-rsapss,
                      sha512-rsapss, dsa-sha1, dsa-sha256, ecdsa-sha1,
                      ecdsa-sha256, ecdsa-sha384, and ecdsa-sha512. Defaults
                      to sha512-rsa.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) != 1 {
			r.ExitWithUsage("x509 reissue")
		}
		if opt.SkipIfExists {
			fmt.Fprintf(os.Stderr, "@R{!!} @C{--no-clobber} @R{is incompatible with} @C{safe x509 reissue}\n")
			r.ExitWithUsage("x509 reissue")
		}

		v := connect(true)

		/* find the Certificate that we want to renew */
		s, err := v.Read(args[0])
		if err != nil {
			return err
		}
		cert, err := s.X509(true)
		if err != nil {
			return err
		}

		if len(opt.X509.Reissue.Name) > 0 {
			ips, dns, email := vault.CategorizeSANs(uniq(opt.X509.Renew.Name))
			cert.Certificate.IPAddresses = ips
			cert.Certificate.DNSNames = dns
			cert.Certificate.EmailAddresses = email
		}

		if opt.X509.Reissue.Subject != "" {
			cert.Certificate.Subject, err = vault.ParseSubject(opt.X509.Reissue.Subject)
			if err != nil {
				return err
			}

			cert.Certificate.RawSubject, err = asn1.Marshal(cert.Certificate.Subject.ToRDNSequence())
			if err != nil {
				return err
			}
		}

		if len(opt.X509.Reissue.KeyUsage) > 0 {
			keyUsage, extKeyUsage, err := vault.HandleJointKeyUsages(opt.X509.Reissue.KeyUsage)
			if err != nil {
				return err
			}

			cert.Certificate.KeyUsage = keyUsage
			cert.Certificate.ExtKeyUsage = extKeyUsage
		}

		if opt.X509.Reissue.SigAlgorithm != "" {
			sigAlgo, err := vault.TranslateSignatureAlgorithm(opt.X509.Reissue.SigAlgorithm)
			if err != nil {
				return err
			}

			cert.Certificate.SignatureAlgorithm = sigAlgo
		}

		/* find the CA */
		ca, caPath, err := v.FindSigningCA(cert, args[0], opt.X509.Reissue.SignedBy)
		if err != nil {
			return err
		}

		// Get new expiry date
		var ttl time.Duration
		if opt.X509.Reissue.TTL == "" {
			ttl = cert.Certificate.NotAfter.Sub(cert.Certificate.NotBefore)
		} else {
			ttl, err = duration(opt.X509.Reissue.TTL)
			if err != nil {
				return err
			}
		}

		// Get signing key bit length
		if opt.X509.Reissue.Bits == 0 {
			opt.X509.Reissue.Bits = cert.PrivateKey.N.BitLen()
		}
		if opt.X509.Reissue.Bits != 1024 && opt.X509.Reissue.Bits != 2048 && opt.X509.Reissue.Bits != 4096 {
			return fmt.Errorf("Bits must be one of 1024, 2048 or 4096")
		}

		// Generate new key with same bit length.
		fmt.Printf("\nGenerating new %d-bit key...\n", opt.X509.Reissue.Bits)
		newKey, err := rsa.GenerateKey(rand.Reader, opt.X509.Reissue.Bits)
		if err != nil {
			return err
		}
		cert.PrivateKey = newKey
		err = ca.Sign(cert, ttl)
		if err != nil {
			return err
		}
		if caPath != args[0] {
			err = ca.SaveTo(v, caPath, false)
			if err != nil {
				return err
			}
		}

		err = cert.SaveTo(v, args[0], false)
		if err != nil {
			return err
		}

		fmt.Printf("Reissued x509 certificate at %s - expiry set to %s\n\n", args[0], cert.ExpiryString())

		return nil
	})

	r.Dispatch("x509 renew", &Help{
		Summary: "Renew X.509 Certificates and Certificate Authorities",
		Usage:   "safe x509 renew [OPTIONS] path/to/certificate",
		Type:    DestructiveCommand,
		Description: `
Renew an X.509 Certificate with existing key

The following options are recognized:
  -s, --subject       The subject name for this certificate.
                      i.e. /cn=www.example.com/c=us/st=ny...
                      Unlike in x509 issue, the subject will not automatically
                      take the first SAN - if you want to update it, you will
                      need to specify this flag explicitly. Use caution when
                      changing the subject of a CA cert, as it will
                      invalidate the chain of trust between the CA and
                      certificates it has signed for many client implementations.

  -n, --name          Subject Alternate Name(s) for this
                      certificate.  These can be domain names,
                      IP addresses or email address -- safe will
                      figure out how to properly encode them.
                      Can (and probably should) be specified
                      more than once. This flag will not append additional SANs,
                      it will act as an exhaustive list in the same way that
                      it would for a new issue command.

  -i, --signed-by   	Path in the Vault where the CA certificate
                      (and signing key) can be found.  If this is not
                      provided, a sibling secret named 'ca' will used
                      if it exists.  This should be the same CA that
                      originally signed the certificate, but does not
                      have to be.

  -t, --ttl           How long the new certificate will be valid
                      for.  Specified in units h (hours), m (months)
                      d (days) or y (years).  1m = 30d and 1y = 365d
                      Defaults to the last TTL used to issue or renew
                      the certificate.

  -u, --key-usage     An x509 key usage or extended key usage. Can be specified
                      once for each desired usage. Valid key usage values are:
                      'digital_signature', 'non_repudiation', 'key_encipherment',
                      'data_encipherment', 'key_agreement', 'key_cert_sign',
                      'crl_sign', 'encipher_only', or 'decipher_only'. Valid
                      extended key usages are 'client_auth', 'server_auth', 'code_signing',
                      'email_protection', or 'timestamping'. The default extended
                      key usages are 'server_auth' and 'client_auth'. CA certs
                      will additionally have the default key usages of key_cert_sign
                      and crl_sign. Specifying any key usages manually will override
                      all of these defaults. To specify no key usages, add 'no' as the
											only key usage.

  -l, --sig-algorithm The algorithm that the certificate will be signed
                      with. Valid values are md5-rsa, sha1-rsa, sha256-rsa
                      sha384-rsa, sha512-rsa, sha256-rsapss, sha384-rsapss,
                      sha512-rsapss, dsa-sha1, dsa-sha256, ecdsa-sha1,
                      ecdsa-sha256, ecdsa-sha384, and ecdsa-sha512. Defaults
                      to sha512-rsa.
`,
	}, func(command string, args ...string) error {
		rc.Apply(opt.UseTarget)

		if len(args) != 1 {
			r.ExitWithUsage("x509 renew")
		}
		if opt.SkipIfExists {
			fmt.Fprintf(os.Stderr, "@R{!!} @C{--no-clobber} @R{is incompatible with} @C{safe x509 renew}\n")
			r.ExitWithUsage("x509 renew")
		}

		v := connect(true)

		/* find the Certificate that we want to renew */
		s, err := v.Read(args[0])
		if err != nil {
			return err
		}
		cert, err := s.X509(true)
		if err != nil {
			return err
		}

		if len(opt.X509.Renew.Name) > 0 {
			ips, dns, email := vault.CategorizeSANs(uniq(opt.X509.Renew.Name))
			cert.Certificate.IPAddresses = ips
			cert.Certificate.DNSNames = dns
			cert.Certificate.EmailAddresses = email
		}

		if opt.X509.Renew.Subject != "" {
			cert.Certificate.Subject, err = vault.ParseSubject(opt.X509.Renew.Subject)
			if err != nil {
				return err
			}

			cert.Certificate.RawSubject, err = asn1.Marshal(cert.Certificate.Subject.ToRDNSequence())
			if err != nil {
				return err
			}
		}

		if len(opt.X509.Renew.KeyUsage) > 0 {
			keyUsage, extKeyUsage, err := vault.HandleJointKeyUsages(opt.X509.Renew.KeyUsage)
			if err != nil {
				return err
			}

			cert.Certificate.KeyUsage = keyUsage
			cert.Certificate.ExtKeyUsage = extKeyUsage
		}

		if opt.X509.Renew.SigAlgorithm != "" {
			sigAlgo, err := vault.TranslateSignatureAlgorithm(opt.X509.Renew.SigAlgorithm)
			if err != nil {
				return err
			}

			cert.Certificate.SignatureAlgorithm = sigAlgo
		}

		/* find the CA */
		ca, caPath, err := v.FindSigningCA(cert, args[0], opt.X509.Renew.SignedBy)
		if err != nil {
			return err
		}

		// Get new expiry date
		var ttl time.Duration
		if opt.X509.Renew.TTL == "" {
			ttl = cert.Certificate.NotAfter.Sub(cert.Certificate.NotBefore)
		} else {
			ttl, err = duration(opt.X509.Renew.TTL)
			if err != nil {
				return err
			}
		}

		err = ca.Sign(cert, ttl)
		if err != nil {
			return err
		}
		if caPath != args[0] {
			err = ca.SaveTo(v, caPath, false)
			if err != nil {
				return err
			}
		}

		err = cert.SaveTo(v, args[0], false)
		if err != nil {
			return err
		}

		fmt.Printf("\nRenewed x509 certificate at %s - expiry set to %s\n\n", args[0], cert.ExpiryString())
		return nil
	})

	r.Dispatch("x509 revoke", &Help{
		Summary: "Revoke X.509 Certificates and Certificate Authorities",
		Usage:   "safe x509 revoke [OPTIONS] path/to/certificate",
		Type:    DestructiveCommand,
		Description: `
Revoke an X.509 Certificate via its Certificate Authority

The following options are recognized:

  -i, --signed-by   Path in the Vault where the CA certificate that
                    signed the certificate to revoke resides.
`,
	}, func(command string, args ...string) error {
		if opt.X509.Revoke.SignedBy == "" || len(args) != 1 {
			r.ExitWithUsage("x509 revoke")
		}

		rc.Apply(opt.UseTarget)
		v := connect(true)

		/* find the CA */
		s, err := v.Read(opt.X509.Revoke.SignedBy)
		if err != nil {
			return err
		}
		ca, err := s.X509(true)
		if err != nil {
			return err
		}

		/* find the Certificate */
		s, err = v.Read(args[0])
		if err != nil {
			return err
		}
		cert, err := s.X509(true)
		if err != nil {
			return err
		}

		/* revoke the Certificate */
		/* FIXME make sure the CA signed this cert */
		ca.Revoke(cert)
		s, err = ca.Secret(false) // SkipIfExists doesnt make sense in the context of revoke
		if err != nil {
			return err
		}

		err = v.Write(opt.X509.Revoke.SignedBy, s)
		if err != nil {
			return err
		}

		return nil
	})

	r.Dispatch("x509 show", &Help{
		Summary: "Show the details of an X.509 Certificate",
		Usage:   "safe x509 show path [path ...]",
		Type:    NonDestructiveCommand,
		Description: `
When dealing with lots of different X.509 Certificates, it is important
to be able to identify what lives at each path in the vault.  This command
prints out information about a certificate, including:

  - Who issued it?
  - Is it a Certificate Authority?
  - What names / IPs is it valid for?
  - When does it expire?

`,
	}, func(command string, args ...string) error {
		if len(args) == 0 {
			r.ExitWithUsage("x509 show")
		}

		rc.Apply(opt.UseTarget)
		v := connect(true)

		for _, path := range args {
			s, err := v.Read(args[0])
			if err != nil {
				return err
			}

			fmt.Printf("%s:\n", path)
			cert, err := s.X509(false)
			if err != nil {
				fmt.Printf("  !! %s\n\n", err)
				continue
			}

			fmt.Printf("  @G{%s}\n\n", cert.Subject())
			if cert.Subject() != cert.Issuer() {
				fmt.Printf("  issued by: @C{%s}\n", cert.Issuer())
				for i := range cert.Intermediaries {
					fmt.Printf("        via: @C{%s}\n", cert.IntermediarySubject(i))
				}
			} else {
				fmt.Printf("  @C{self-signed}\n")
			}

			toStart := cert.Certificate.NotBefore.Sub(time.Now())
			toEnd := cert.Certificate.NotAfter.Sub(time.Now())

			days := int(toStart.Hours() / 24)
			if days == 1 {
				fmt.Printf("  @Y{not valid for another day}\n")
			} else if days > 1 {
				fmt.Printf("  @Y{not valid for another %d days}\n", days)
			}

			days = int(toEnd.Hours() / 24)
			if days < -1 {
				fmt.Printf("  @R{EXPIRED %d days ago}\n", -1*days)
			} else if days < 0 {
				fmt.Printf("  @R{EXPIRED a day ago}\n")
			} else if days < 1 {
				fmt.Printf("  @R{EXPIRED}\n")
			} else if days == 1 {
				fmt.Printf("  @Y{expires in a day}\n")
			} else if days < 30 {
				fmt.Printf("  @Y{expires in %d days}\n", days)
			} else {
				fmt.Printf("  expires in @G{%d days}\n", days)
			}
			fmt.Printf("  valid from @C{%s} - @C{%s}", cert.Certificate.NotBefore.Format("Jan 2 2006"), cert.Certificate.NotAfter.Format("Jan 2 2006"))

			life := int(cert.Certificate.NotAfter.Sub(cert.Certificate.NotBefore).Hours())
			if life < 360*24 {
				fmt.Printf(" (@M{~%d days})\n", life/24)
			} else {
				fmt.Printf(" (@M{~%d years})\n", life/365/24)
			}
			fmt.Printf("\n")

			n := 0
			fmt.Printf("  for the following purposes:\n")
			if cert.KeyUsage&x509.KeyUsageDigitalSignature != 0 {
				n++
				fmt.Printf("    - @C{digital-signature}  can be used to verify digital signatures.\n")
			}
			if cert.KeyUsage&x509.KeyUsageContentCommitment != 0 {
				n++
				fmt.Printf("    - @C{non-repudiation}    can be used for non-repudiation / content commitment.\n")
			}
			if cert.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
				n++
				fmt.Printf("    - @C{key-encipherment}   can be used encrypt other keys, for transport.\n")
			}
			if cert.KeyUsage&x509.KeyUsageDataEncipherment != 0 {
				n++
				fmt.Printf("    - @C{data-encipherment}  can be used to encrypt user data directly.\n")
			}
			if cert.KeyUsage&x509.KeyUsageKeyAgreement != 0 {
				n++
				fmt.Printf("    - @C{key-agreement}      can be used in key exchange, a la Diffie-Hellman key exchange.\n")
			}
			if cert.KeyUsage&x509.KeyUsageCertSign != 0 {
				n++
				fmt.Printf("    - @C{key-cert-sign}      can be used to verify digital signatures on public key certificates.\n")
			}
			if cert.KeyUsage&x509.KeyUsageCRLSign != 0 {
				n++
				fmt.Printf("    - @C{crl-sign}           can be used to verify digital signatures on certificate revocation lists.\n")
			}
			if cert.KeyUsage&x509.KeyUsageEncipherOnly != 0 {
				n++
				if cert.KeyUsage&x509.KeyUsageKeyAgreement != 0 {
					fmt.Printf("    - @C{encipher-only}      can only be used to encrypt data in a key exchange.\n")
				} else {
					fmt.Printf("    - @C{encipher-only}      this key-usage is undefined if key-agreement is not set (which it isn't).\n")
				}
			}
			if cert.KeyUsage&x509.KeyUsageDecipherOnly != 0 {
				n++
				if cert.KeyUsage&x509.KeyUsageKeyAgreement != 0 {
					fmt.Printf("    - @C{decipher-only}      can only be used to decrypt data in a key exchange.\n")
				} else {
					fmt.Printf("    - @C{decipher-only}      this key-usage is undefined if key-agreement is not set (which it isn't).\n")
				}
			}
			for _, ku := range cert.ExtKeyUsage {
				n++
				switch ku {
				default:
					n--
				case x509.ExtKeyUsageClientAuth:
					fmt.Printf("    - @C{client-auth}*       can be used by a TLS client for authentication.\n")
				case x509.ExtKeyUsageServerAuth:
					fmt.Printf("    - @C{server-auth}*       can be used by a TLS server for authentication.\n")
				case x509.ExtKeyUsageCodeSigning:
					fmt.Printf("    - @C{code-signing}*      can be used to sign software packages to prove source.\n")
				case x509.ExtKeyUsageEmailProtection:
					fmt.Printf("    - @C{email-protection}*  can be used to protect email (signing, encryption, and key exchange).\n")
				case x509.ExtKeyUsageTimeStamping:
					fmt.Printf("    - @C{timestamping}*      can be used to generate trusted timestamps.\n")
				}
			}
			if n == 0 {
				fmt.Printf("    (no special key usage constraints present)\n")
			}
			fmt.Printf("\n")

			fmt.Printf("  signed with the algorithm ")
			sigView := map[x509.SignatureAlgorithm]string{
				x509.UnknownSignatureAlgorithm: "Unknown",
				x509.MD2WithRSA:                "MD2 With RSA",
				x509.MD5WithRSA:                "MD5 With RSA",
				x509.SHA1WithRSA:               "SHA1 With RSA",
				x509.SHA256WithRSA:             "SHA256 With RSA",
				x509.SHA384WithRSA:             "SHA384 With RSA",
				x509.SHA512WithRSA:             "SHA512 With RSA",
				x509.DSAWithSHA1:               "DSA With SHA1",
				x509.DSAWithSHA256:             "DSA With SHA256",
				x509.ECDSAWithSHA1:             "ECDSA With SHA1",
				x509.ECDSAWithSHA256:           "ECDSA With SHA256",
				x509.ECDSAWithSHA384:           "ECDSA With SHA384",
				x509.ECDSAWithSHA512:           "ECDSA With SHA512",
				x509.SHA256WithRSAPSS:          "SHA256 With RSAPSS",
				x509.SHA384WithRSAPSS:          "SHA384 With RSAPSS",
				x509.SHA512WithRSAPSS:          "SHA512 With RSAPSS",
			}
			sigAlgo := sigView[cert.Certificate.SignatureAlgorithm]
			fmt.Printf("@G{%s}\n", sigAlgo)
			fmt.Printf("\n")

			fmt.Printf("  for the following names:\n")
			for _, s := range cert.Certificate.DNSNames {
				fmt.Printf("    - @G{%s} (DNS)\n", s)
			}
			for _, s := range cert.Certificate.EmailAddresses {
				fmt.Printf("    - @G{%s} (email)\n", s)
			}
			for _, s := range cert.Certificate.IPAddresses {
				fmt.Printf("    - @G{%s} (IP)\n", s)
			}
			fmt.Printf("\n")

			serialString := fmt.Sprintf("@M{%[1]d} (@M{%#[1]x})", cert.Certificate.SerialNumber)
			if cert.Certificate.SerialNumber.Cmp(big.NewInt(1000)) == 1 {
				serialString = fmt.Sprintf("@M{%s}", cert.FormatSerial())
			}
			fmt.Printf("  serial: %s\n", serialString)
			fmt.Printf("  ")
			if cert.IsCA() {
				fmt.Printf("@G{is}")
			} else {
				fmt.Printf("@Y{is not}")
			}
			fmt.Printf(" a CA\n")
			fmt.Printf("\n")
		}

		return nil
	})

	r.Dispatch("x509 crl", &Help{
		Summary: "Manage a X.509 Certificate Authority Revocation List",
		Usage:   "safe x509 crl --renew path",
		Type:    DestructiveCommand,
		Description: `
Each X.509 Certificate Authority (especially those generated by
'safe issue --ca') carries with a list of certificates it has revoked,
by certificate serial number.  This command lets you manage that CRL.

Currently, only the --renew option is supported, and it is required:

  --renew           Sign and update the validity dates of the CRL,
                    without modifying the list of revoked certificates.
`,
	}, func(command string, args ...string) error {
		if !opt.X509.CRL.Renew || len(args) != 1 {
			r.ExitWithUsage("x509 crl")
		}

		rc.Apply(opt.UseTarget)
		v := connect(true)

		s, err := v.Read(args[0])
		if err != nil {
			return err
		}
		ca, err := s.X509(true)
		if err != nil {
			return err
		}

		if !ca.IsCA() {
			return fmt.Errorf("%s is not a certificate authority", args[0])
		}

		/* simply re-saving the CA X509 object regens the CRL */
		s, err = ca.Secret(false) // SkipIfExists doesn't make sense in the context of crl regeneration
		if err != nil {
			return err
		}
		err = v.Write(args[0], s)
		if err != nil {
			return err
		}

		return nil
	})

	env.Override(&opt)
	p, err := cli.NewParser(&opt, os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "@R{!! %s}\n", err)
		os.Exit(1)
	}

	if opt.Version {
		r.Execute("version")
		return
	}
	if opt.Help { //-h was given as a global arg
		r.Execute("help")
		return
	}

	for p.Next() {
		opt.SkipIfExists = !opt.Clobber

		if opt.Version {
			r.Execute("version")
			return
		}

		if p.Command == "" { //No recognized command was found
			r.Execute("help")
			return
		}

		if opt.Help { // -h or --help was given after a command
			r.Execute("help", p.Command)
			continue
		}

		os.Unsetenv("VAULT_SKIP_VERIFY")
		os.Unsetenv("SAFE_SKIP_VERIFY")
		if opt.Insecure {
			os.Setenv("VAULT_SKIP_VERIFY", "1")
			os.Setenv("SAFE_SKIP_VERIFY", "1")
		}

		defer rc.Cleanup()
		err = r.Execute(p.Command, p.Args...)
		if err != nil {
			if strings.HasPrefix(err.Error(), "USAGE") {
				fmt.Fprintf(os.Stderr, "@Y{%s}\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "@R{!! %s}\n", err)
			}
			os.Exit(1)
		}
	}

	//If there were no args given, the above loop that would try to give help
	// doesn't execute at all, so we catch it here.
	if p.Command == "" {
		r.Execute("help")
	}

	if err = p.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "@R{!! %s}\n", err)
		os.Exit(1)
	}
}

func recursively(cmd string, args ...string) bool {
	y := prompt.Normal("Recursively @R{%s} @C{%s} @Y{(y/n)} ", cmd, strings.Join(args, " "))
	y = strings.TrimSpace(y)
	return y == "y" || y == "yes"
}

//For versions of safe 0.10+
// Older versions just use a map[string]map[string]string
type exportFormat struct {
	ExportVersion uint `json:"export_version"`
	//map from path string to map from version number to version info
	Data               map[string]exportSecret `json:"data"`
	RequiresVersioning map[string]bool         `json:"requires_versioning"`
}

type exportSecret struct {
	FirstVersion uint            `json:"first,omitempty"`
	Versions     []exportVersion `json:"versions"`
}

type exportVersion struct {
	Deleted   bool              `json:"deleted,omitempty"`
	Destroyed bool              `json:"destroyed,omitempty"`
	Value     map[string]string `json:"value,omitempty"`
}
