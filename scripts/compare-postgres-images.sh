#!/bin/sh
# Compares two PostgreSQL pool images for a patch transition review
# (docs/postgres-patch-updates.md): the server version, OS base, collation
# libraries and the packaged extension versions, plus what each changed
# extension's upgrade scripts would run. Lines starting with BLOCK name a
# difference that rules out an in-place patch update.
#
# usage: scripts/compare-postgres-images.sh FROM_IMAGE TO_IMAGE
# PLATFORM=linux/amd64 selects the platform to pull and inspect.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $0 FROM_IMAGE TO_IMAGE" >&2
	exit 2
fi
from=$1
to=$2
platform=${PLATFORM:-}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

run() {
	image=$1
	shift
	if [ -n "$platform" ]; then
		docker run --rm --platform "$platform" --entrypoint sh "$image" -c "$@"
	else
		docker run --rm --entrypoint sh "$image" -c "$@"
	fi
}

facts() {
	run "$1" '
		major=$(postgres --version | sed -E "s/.* ([0-9]+)\..*/\1/")
		echo "postgres=$(postgres --version | sed -E "s/.* ([0-9.]+) .*/\1/")"
		echo "major=$major"
		echo "debian=$(cat /etc/debian_version)"
		echo "glibc=$(ldd --version | head -1 | sed -E "s/.* ([0-9.]+)$/\1/")"
		echo "icu=$(ls /usr/lib/*-linux-gnu/ | sed -nE "s/^libicuuc\.so\.([0-9]+)$/\1/p")"
	'
}

extensions() {
	run "$1" '
		major=$(postgres --version | sed -E "s/.* ([0-9]+)\..*/\1/")
		cd /usr/share/postgresql/$major/extension
		for control in *.control; do
			echo "${control%.control}=$(sed -nE "s/^default_version *= *.([^'"'"']*).*/\1/p" "$control")"
		done
	'
}

for image in "$from" "$to"; do
	if [ -n "$platform" ]; then
		docker pull -q --platform "$platform" "$image" >/dev/null
	else
		docker pull -q "$image" >/dev/null
	fi
done

facts "$from" >"$work/from-facts"
facts "$to" >"$work/to-facts"
extensions "$from" | sort >"$work/from-ext"
extensions "$to" | sort >"$work/to-ext"

value() { sed -n "s/^$2=//p" "$1"; }

echo "                from                              to"
for key in postgres major debian glibc icu; do
	printf '%-10s %-33s %s\n' "$key" "$(value "$work/from-facts" "$key")" "$(value "$work/to-facts" "$key")"
done

status=0
for key in major glibc icu; do
	if [ "$(value "$work/from-facts" "$key")" != "$(value "$work/to-facts" "$key")" ]; then
		echo "BLOCK: $key changes; this is not an in-place patch update"
		status=1
	fi
done

echo
echo "Extension default versions that change:"
join -t= "$work/from-ext" "$work/to-ext" | awk -F= '$2 != $3 { print "  " $1 ": " $2 " -> " $3 }' >"$work/changed"
comm -23 "$work/from-ext" "$work/to-ext" | cut -d= -f1 | while read -r name; do
	grep -q "^$name=" "$work/to-ext" || echo "  $name: removed"
done >>"$work/changed"
if [ -s "$work/changed" ]; then
	cat "$work/changed"
else
	echo "  none"
fi

# Statements an ALTER EXTENSION ... UPDATE would run between the two
# packaged versions, comments and psql guards left out. Empty means the
# shared library carries the whole change.
major=$(value "$work/to-facts" major)
upgrade_path='
	name=$1 version=$2 target=$3
	cd "/usr/share/postgresql/$4/extension"
	while [ "$version" != "$target" ]; do
		script=$(ls "$name--$version--"*.sql 2>/dev/null | head -1)
		if [ -z "$script" ]; then
			echo "  no upgrade path from $version"
			break
		fi
		statements=$(grep -vE "^[[:space:]]*(--|\\\\echo|COMMENT ON|$)" "$script" || true)
		if [ -n "$statements" ]; then
			echo "  $script:"
			echo "$statements" | sed "s/^/    /"
		else
			echo "  $script: comments only"
		fi
		version=${script#"$name--$version--"}
		version=${version%.sql}
	done
'
awk -F'[ :>-]+' '/->/ { print $2, $3, $4 }' "$work/changed" | while read -r name old new; do
	echo
	echo "Upgrade scripts for $name $old -> $new:"
	run "$to" "$upgrade_path" sh "$name" "$old" "$new" "$major"
done

exit "$status"
