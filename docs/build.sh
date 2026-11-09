#!/bin/sh
# Builds the deployable docs site in docs/public (Vercel serves that folder).
set -e
cd "$(dirname "$0")"
hash=$(find assets -type f | sort | xargs cksum | cksum | awk '{print $1}')
mkdir -p public
sed \
  -e "s|assets/fonts/fonts.css\"|assets/fonts/fonts.css?v=$hash\"|g" \
  -e "s|assets/site.css\"|assets/site.css?v=$hash\"|g" \
  -e "s|assets/brand.css\"|assets/brand.css?v=$hash\"|g" \
  -e "s|assets/site.js\"|assets/site.js?v=$hash\"|g" \
  -e "s|assets/vendor/highlight/highlight.min.js\"|assets/vendor/highlight/highlight.min.js?v=$hash\"|g" \
  -e "s|assets/vendor/highlight/go.min.js\"|assets/vendor/highlight/go.min.js?v=$hash\"|g" \
  site.html > public/index.html
rm -rf public/assets
cp -R assets public/assets
cp static/404.html static/robots.txt static/sitemap.xml static/site.webmanifest static/vercel.json public/
echo "built docs/public"
