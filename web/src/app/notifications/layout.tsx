import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "Live Activity",
  description: "New releases, trailers and recommendations",
};

export default function NotificationsLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return children;
}