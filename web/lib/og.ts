// The one share image every page advertises. app/opengraph-image.tsx
// renders it from these constants and lib/trust-metadata.ts names it
// in every page's Open Graph and Twitter metadata, so the image
// survives Next replacing a page's openGraph object wholesale.

export const OG_IMAGE_ALT = "agentcookie: your agent's session state, synced.";
export const OG_IMAGE_SIZE = { width: 1200, height: 630 };

export const OG_IMAGE = {
  url: "/opengraph-image",
  width: OG_IMAGE_SIZE.width,
  height: OG_IMAGE_SIZE.height,
  alt: OG_IMAGE_ALT,
};
