#!/bin/sh
# Stand-in kernel for the file transport: reads in.bin, writes its length to out.bin.
set -eu
size=$(wc -c < in.bin | tr -d ' ')
printf '%s\n' "$size" > out.bin
