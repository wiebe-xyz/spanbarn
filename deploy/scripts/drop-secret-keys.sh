#!/bin/sh
# Remove retired keys from the app Secret after the SOPS manifest is applied.
#
#   drop-secret-keys.sh NAMESPACE SECRET KEYS_FILE
#
# The SOPS manifests set the Secret through stringData. When a key leaves the
# manifest, `kubectl apply` drops it from stringData in its patch, but the API
# server stores stringData as data, so the key stays on the live Secret and
# every pod still gets it through envFrom. This removes each key listed in
# KEYS_FILE (one per line, # starts a comment) from the live Secret's data.
# A key that is already gone is skipped, so the step can run on every deploy.
# Only key names are read; values are never printed.
set -eu

[ "$#" -eq 3 ] || { echo "usage: $0 NAMESPACE SECRET KEYS_FILE" >&2; exit 2; }
ns="$1"
secret="$2"
keys_file="$3"

# shellcheck disable=SC2016 # a go-template, not shell expansion
present=$(kubectl -n "$ns" get secret "$secret" \
	-o go-template='{{range $k, $v := .data}}{{$k}}{{"\n"}}{{end}}')

sed -e 's/#.*//' -e 's/[[:space:]]//g' "$keys_file" | while read -r key; do
	[ -n "$key" ] || continue
	if printf '%s\n' "$present" | grep -qx "$key"; then
		kubectl -n "$ns" patch secret "$secret" --type=json \
			-p "[{\"op\":\"remove\",\"path\":\"/data/${key}\"}]"
		echo "removed ${key} from ${ns}/${secret}"
	else
		echo "${key} not in ${ns}/${secret}"
	fi
done
