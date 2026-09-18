import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import { SITE_NAME, SITE_TITLE, SITE_DESCRIPTION } from "@/lib/content/home";
import { SITE_ORIGIN } from "@/lib/site";

// Geist Sans + Geist Mono. The CSS variables are consumed by
// `app/globals.css` to drive --font-body and --font-display, which
// in turn power Tailwind's `font-body` / `font-display` utilities.
const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: SITE_TITLE,
  description: SITE_DESCRIPTION,
  // Literal production origin. Preview deployments must still emit
  // canonical agentcookie.dev URLs, so no environment fallback here.
  metadataBase: new URL(SITE_ORIGIN),
  openGraph: {
    title: SITE_TITLE,
    description: SITE_DESCRIPTION,
    url: "/",
    siteName: SITE_NAME,
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
    title: SITE_TITLE,
    description: SITE_DESCRIPTION,
  },
  // is-agentic.com reads this to grade the site as a content site
  // rather than an app (R9).
  other: { "is-agentic-site-type": "content" },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className={`${geistSans.variable} ${geistMono.variable}`}>
        {children}
      </body>
    </html>
  );
}
