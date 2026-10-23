/** Operational alerts (server-go internal/service/alerts) and the monitoring status. */

export type AlertSeverity = 'info' | 'warning' | 'critical';

export type AlertSource =
  | 'usdt'
  | 'hedging'
  | 'reconciliation'
  | 'exposure'
  | 'feed'
  | 'settlement'
  | 'rate-limiter'
  | 'system';

export interface OpsAlert {
  id: string;
  dedupKey: string;
  code: string;
  source: AlertSource;
  severity: AlertSeverity;
  title: string;
  detail: string;
  status: 'open' | 'resolved';
  /** Sightings while open; a repeat never opens a second alert. */
  occurrences: number;
  firstSeenAt: string;
  lastSeenAt: string;
  /** An email about it was queued. */
  notified: boolean;
  resolvedBy: string | null;
  resolvedAt: string | null;
  resolutionNote: string | null;
  /** The condition cleared by itself. */
  resolvedBySystem: boolean;
}

export interface AlertFilters {
  status?: 'open' | 'resolved';
  severity?: AlertSeverity;
  source?: AlertSource;
  page?: number;
  limit?: number;
}

export interface AlertingStatus {
  alerting: {
    emailEnabled: boolean;
    minSeverity: AlertSeverity;
    recipients: number;
    maxEmailsPerHour: number;
    open: number;
    openCritical: number;
    pendingNotifications: number;
    failedNotifications: number;
    sentLastHour: number;
    lastSentAt: string | null;
    lastError: string | null;
  };
  exposureBook: {
    status: string;
    generation?: string;
    reason?: string;
    builtAt?: number | null;
    lastReconciledAt?: number | null;
    pendingReservations?: number;
    error: string | null;
  };
  rateLimiter: {
    mode: 'redis' | 'memory';
    degraded: boolean;
    fallbackRequests: number;
    degradedSince: number;
  };
  /** Conditions this instance's monitor currently sees as bad. */
  conditions: string[];
}
