package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/privacyfence/peek/internal/version"
)

// commandNames are the commands Run dispatches to a cmdXxx function; each one
// answers --help and -h with its own usage lines.
var commandNames = []string{
	"login", "config", "upload", "list", "delete", "visibility", "rename",
	"grant", "revoke", "grants", "versions", "stats", "comments", "export",
	"delete-all", "token", "provisioner", "backend",
}

// wantsHelp reports whether args ask for help.
func wantsHelp(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return a == "-h" || a == "--help" })
}

// commandUsage returns the usage lines of one command.
func commandUsage(cmd string) string {
	var b strings.Builder
	b.WriteString("Usage:\n")
	for _, line := range strings.Split(usageText, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "peek "+cmd+" ") || t == "peek "+cmd {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// Run dispatches a command. args is argv without the program name.
func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	cmd, rest := args[0], args[1:]
	if slices.Contains(commandNames, cmd) && wantsHelp(rest) {
		fmt.Fprint(os.Stderr, commandUsage(cmd))
		return 0
	}
	var err error
	switch cmd {
	case "login":
		err = cmdLogin(rest)
	case "config":
		err = cmdConfig(rest)
	case "upload":
		err = cmdUpload(rest)
	case "list":
		err = cmdList(rest)
	case "delete":
		err = cmdDelete(rest)
	case "visibility":
		err = cmdVisibility(rest)
	case "rename":
		err = cmdRename(rest)
	case "grant":
		err = cmdGrant(rest)
	case "revoke":
		err = cmdRevoke(rest)
	case "grants":
		err = cmdGrants(rest)
	case "versions":
		err = cmdVersions(rest)
	case "stats":
		err = cmdStats(rest)
	case "comments":
		err = cmdComments(rest)
	case "export":
		err = cmdExport(rest)
	case "delete-all":
		err = cmdDeleteAll(rest)
	case "token":
		err = cmdToken(rest)
	case "provisioner":
		err = cmdProvisioner(rest)
	case "backend":
		err = cmdBackend(rest)
	case "help", "-h", "--help":
		usage()
		return 0
	case "version", "-v", "--version":
		fmt.Println("peek " + version.String())
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		return 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func usage() { fmt.Fprint(os.Stderr, usageText) }

const usageText = `peek — Peek CLI

Usage:
  peek login [--host <url>]             sign in with browser login when available
  peek login --token-stdin              read an access token from stdin
  peek login --oauth                    always use browser login; fails when the server has none
  peek config set --host <url>          set host (use 'login' / --token-stdin for the token)
  peek config show
  peek upload <file.html> [--name <filename>]
  peek upload <file.html> --visibility public|signed-in
  peek upload <file.html> --visibility restricted
  peek upload <file.html> --slug <slug>        upload a new version of an existing page
  peek upload <file.html> --backend <image> [--backend-port <n>]  attach a backend container
  peek list
  peek delete <slug>
  peek visibility <slug> public
  peek visibility <slug> signed-in
  peek visibility <slug> restricted
  peek rename <slug> <new name>        rename a page; its link follows the name and old links redirect
  peek grant <slug> <email> [--group]  give a user (or Google group) access to a restricted page
  peek revoke <slug> <email> [--group] remove a grant
  peek grants <slug>                   list grants on a page
  peek backend <slug>                  show backends and environment variable names
  peek backend set <slug> <image> [--version <n>|latest] [--port <n>] [--allow-token-calls]
  peek backend remove <slug> [--version <n>|latest]
  peek backend env set <slug> <NAME> --value <v>|--value-stdin   set a backend environment variable
  peek backend env unset <slug> <NAME>
  peek backend restart <slug>          stop the backend so it restarts with new variables
  peek backend logs <slug> [--version <n>|latest] [--tail <n>]
  peek backend token-calls on|off <slug>   allow or refuse API-token calls to the backend
  peek backend call <slug> <path> [--method M] [--data <json|@file|->] [--version <n>|latest] [-H "Name: value"]   call the backend with your token
  peek backend data export <slug> [-o <file>]   download the page's /data volume as a tar archive
  peek backend data import <slug> <file> [--yes]   replace the page's /data volume with a tar archive
  peek stats <slug>
  peek versions <slug>                 list versions of one of your pages
  peek comments <slug> [--version <n>]  list comments (latest version by default)
  peek export <slug>                   export all data for an upload
  peek delete-all                      delete all your uploads
  peek token create --name <name>      create a new user token (admin only)
  peek token list                      list tokens (admin only)
  peek token revoke <id>               revoke a token by id (admin only)
  peek provisioner create --name <n>   create a provisioning key (admin only)
  peek provisioner list                list provisioning keys (admin only)
  peek provisioner revoke <id>         revoke a provisioning key (admin only)
  peek version                         print the CLI version

Token input (most secure first):
  peek login                           browser login when available; otherwise hidden prompt
  peek login --token-stdin             read token from a pipe
  peek login --token-file <path>       read token from a file
  peek login --token <token>           discouraged: exposed in history & 'ps'
  PEEK_TOKEN=…  (env override)          handy for CI
  peek config set --token-stdin        read token from a pipe
  peek config set --token-file <path>  read token from a file
  peek config set --token <token>      discouraged: exposed in history & 'ps'

Environment overrides:
  PEEK_HOST, PEEK_TOKEN
`
