# Static files

Anything in here is served from the site root as-is — `public/hero.jpg` becomes
`/hero.jpg`. Vite does not process these files, so they are not hashed and not
bundled; that is what makes this the right place for an asset someone swaps
without touching the code.

## hero.jpg — the landing page's background photograph

The hero on `/` looks for `/hero.jpg`. If the file is there it is drawn behind
the headline; if it is not, the hero falls back to the gradient backdrop alone
and nothing looks broken. That is deliberate — the repository does not ship a
stock photograph, because a face on a trading site reads as a customer or an
endorsement and choosing one is the site owner's decision.

To add one, save it as `client/public/hero.jpg`.

What works in that slot:

- **Shape** — portrait or square, roughly 1600 x 1800 or larger. It is drawn at
  full height and centred, so a wide landscape photo will be cropped hard at
  the sides.
- **Subject** — centred, with the head in the upper third. The headline sits
  across the middle of the image, so leave the centre reasonably plain.
- **Background** — dark. The photo is dimmed and faded to black at the top and
  bottom to keep the nav and the headline readable, and a bright or busy
  background fights that.
- **Weight** — keep it under about 400 KB. It is the first thing a visitor
  downloads. A `.webp` at the same path also works if the filename is changed
  to match in `HERO_PHOTO` (`client/src/pages/Landing.tsx`).

Make sure you hold the rights to whatever you use. A licensed stock photo, a
commissioned shoot or your own photography are all fine; an image found through
a search engine usually is not.

## favicon.svg - the browser tab icon

The "BT" mark from the top bar, as an SVG so it stays sharp at every size.
Replace it with the brand's own mark when there is one; `index.html` links it.
