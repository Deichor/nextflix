import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: "Who's watching?",
};

export default function ProfilesLayout({ children }: { children: React.ReactNode }) {
  return children;
}
