import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { headers } from "next/headers";
import "./globals.css";

const geistSans = Geist({ variable: "--font-geist-sans", subsets: ["latin"] });
const geistMono = Geist_Mono({ variable: "--font-geist-mono", subsets: ["latin"] });

export async function generateMetadata(): Promise<Metadata> {
  const requestHeaders = await headers();
  const host = requestHeaders.get("x-forwarded-host") ?? requestHeaders.get("host");
  const protocol = requestHeaders.get("x-forwarded-proto") ?? (host?.includes("localhost") ? "http" : "https");
  const socialImage = host ? `${protocol}://${host}/og.png` : undefined;

  return {
    title: "ContentFlow · Your content, in one place",
    description: "A focused workspace for writing, organising, and repurposing content.",
    openGraph: {
      title: "ContentFlow",
      description: "Your content, in one place",
      type: "website",
      images: socialImage ? [{ url: socialImage, width: 1200, height: 630, alt: "ContentFlow content workspace" }] : undefined,
    },
    twitter: {
      card: "summary_large_image",
      title: "ContentFlow",
      description: "Your content, in one place",
      images: socialImage ? [socialImage] : undefined,
    },
  };
}

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body className={`${geistSans.variable} ${geistMono.variable}`}>{children}</body>
    </html>
  );
}
