#!/usr/bin/env bash
set -euo pipefail

tag="${1:?usage: update-image-reference.sh <tag> <image-prefix>}"
image_prefix="${2:?usage: update-image-reference.sh <tag> <image-prefix>}"
manifest="deploy/quickstart.yaml"
image="${image_prefix}/binpacked:${tag}"

sed -i "s#image: .*#image: ${image}#" "${manifest}"
