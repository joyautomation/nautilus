#!/bin/sh
# askpass-bw.sh: an SSH_ASKPASS program for naut deploy that answers ssh's
# password prompt from Bitwarden, for a box whose sshd refuses keys (a
# PLCnext refuses them for admin). naut deploy runs ssh with it, once per
# command (ControlMaster):
#
#   NAUT_BW_ITEM=<item name or id> NAUT_SSH_ASKPASS=tools/deploy/askpass-bw.sh \
#     naut deploy update admin@plc
#
# The vault must already be unlocked: BW_SESSION set, or NAUT_BW_SESSION_FILE
# naming a file that holds the session key. It never unlocks, locks or
# prompts (bw --nointeraction); a locked vault fails with bw's message.
#
# The password goes to stdout, which is ssh's alone, and nowhere else. Any
# prompt that is not a password prompt (an unknown host key's yes/no, a
# confirmation) is refused, so the password is never the answer to one:
# connect once by hand, or ssh-keyscan, so the host key is known first.
set -eu
case ${SSH_ASKPASS_PROMPT:-} in
confirm | none) exit 1 ;;
esac
case ${1:-} in
*[Pp]assword:*) ;;
*)
	echo "askpass-bw: not a password prompt, refused: ${1:-}" >&2
	exit 1
	;;
esac
item=${NAUT_BW_ITEM:?askpass-bw: set NAUT_BW_ITEM to the Bitwarden item holding the password}
if [ -z "${BW_SESSION:-}" ] && [ -n "${NAUT_BW_SESSION_FILE:-}" ]; then
	BW_SESSION=$(cat "$NAUT_BW_SESSION_FILE")
	export BW_SESSION
fi
exec bw --nointeraction get password "$item"
