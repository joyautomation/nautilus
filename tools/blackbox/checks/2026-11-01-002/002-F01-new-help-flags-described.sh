#!/usr/bin/env bash
# F-01: every flag `naut new --help` lists carries a description. Today `-no-go` has none.
set -u
out=$(naut new --help 2>&1); echo "$out"
# Go flag usage: a flag line "  -name [type]" is followed by an indented description line.
bad=$(echo "$out" | awk '/^  -/{f=$1; getline d; if (d !~ /[A-Za-z]/) print f}')
[ -z "$bad" ] && { echo "PASS every flag described"; exit 0; } || { echo "FAIL undescribed: $bad"; exit 1; }
