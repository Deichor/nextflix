"use client";

import React from 'react';
import { useT } from '@/shared/i18n';

interface ErrorBoundaryState {
  hasError: boolean;
  error?: Error;
}

interface ErrorBoundaryProps {
  children: React.ReactNode;
  fallback?: React.ReactNode;
}

class ErrorBoundary extends React.Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { hasError: false };
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: React.ErrorInfo) {
    console.error('Error Boundary caught an error:', error, errorInfo);
  }

  render() {
    if (this.state.hasError) {
      return this.props.fallback || (
        <ErrorFallback error={this.state.error} onReset={() => this.setState({ hasError: false })} />
      );
    }

    return this.props.children;
  }
}

// Function component so the default fallback can use translations
function ErrorFallback({ error, onReset }: { error?: Error; onReset: () => void }) {
  const t = useT();
  return (
    <div className="w-full p-4 bg-red-900/20 rounded-lg">
      <h2 className="text-red-400 text-lg font-semibold mb-2">{t('error.title')}</h2>
      <p className="text-red-300 text-sm">
        {error?.message || t('error.unexpected')}
      </p>
      <button
        onClick={onReset}
        className="mt-2 px-4 py-2 bg-red-600 text-white rounded hover:bg-red-700"
      >
        {t('error.tryAgain')}
      </button>
    </div>
  );
}

export default ErrorBoundary;