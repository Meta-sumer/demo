import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { FileText, ImageIcon, X } from 'lucide-react';
import { api } from '../services/api';
import { ApiRequestError, loadTokens } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatMoney } from '../utils/format';
import { Pagination, Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';
import type { Transfer } from '../types/api';

/**
 * Manual deposit and withdrawal review (spec section 19).
 *
 * A deposit is an unverified claim until an operator matches its UTR or
 * screenshot against the bank statement; approving is what credits the wallet.
 * A withdrawal is approved only after the operator has actually paid, and the
 * reference of that payment is required so the payout is provable later.
 */
export function TransfersPage({ kind }: { kind: 'DEPOSIT' | 'WITHDRAWAL' }) {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('PENDING');
  const [utr, setUtr] = useState('');
  const [preview, setPreview] = useState<Transfer | null>(null);
  const [approving, setApproving] = useState<Transfer | null>(null);

  const isDeposit = kind === 'DEPOSIT';
  const approvePermission = isDeposit ? 'deposits:approve' : 'withdrawals:approve';

  const query = useQuery({
    queryKey: ['transfers', kind, { page, status, utr }],
    queryFn: () =>
      (isDeposit ? api.finance.deposits : api.finance.withdrawals)({
        page,
        limit: 20,
        status: status || undefined,
        utr: utr.trim() || undefined,
      }),
    refetchInterval: 15_000,
    placeholderData: (previous) => previous,
  });

  const approveDeposit = useMutation({
    mutationFn: (id: string) => api.finance.approveDeposit(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['transfers'] });
      toast('success', 'Deposit approved', 'The wallet has been credited');
    },
    onError: (err) => toast('error', 'Approval failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const approveWithdrawal = useMutation({
    mutationFn: (input: { id: string; utr: string; note?: string }) =>
      api.finance.approveWithdrawal(input.id, { utr: input.utr, note: input.note }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['transfers'] });
      setApproving(null);
      toast('success', 'Withdrawal approved', 'The reserved amount has been debited');
    },
    onError: (err) => toast('error', 'Approval failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const rejectMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) => api.finance.reject(id, note),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['transfers'] });
      toast('success', 'Request rejected', 'The customer sees the reason you gave');
    },
    onError: (err) => toast('error', 'Rejection failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  return (
    <>
      <Panel className="overflow-hidden">
        <PanelHeader
          title={isDeposit ? 'Deposits' : 'Withdrawals'}
          description={
            isDeposit
              ? 'Verify the UTR or screenshot against the bank statement before approving - approving credits the wallet'
              : 'Pay the customer first, then approve with the reference of the payment you made'
          }
          actions={
            <div className="flex gap-2">
              <input
                className="field w-44 py-1.5 font-mono"
                placeholder="Search by UTR"
                value={utr}
                onChange={(event) => {
                  setUtr(event.target.value);
                  setPage(1);
                }}
              />
              <select
                className="field w-36 py-1.5"
                value={status}
                onChange={(event) => {
                  setStatus(event.target.value);
                  setPage(1);
                }}
              >
                <option value="">All</option>
                <option value="PENDING">Pending</option>
                <option value="COMPLETED">Completed</option>
                <option value="REJECTED">Rejected</option>
                <option value="FAILED">Failed</option>
              </select>
            </div>
          }
        />

        {query.isLoading ? (
          <TableSkeleton rows={8} columns={7} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[1080px]">
              <thead>
                <tr>
                  <th>Reference</th>
                  <th>User</th>
                  <th className="text-right">Amount</th>
                  <th>{isDeposit ? 'Paid via' : 'Pay to'}</th>
                  <th>UTR</th>
                  <th>Proof</th>
                  <th>Status</th>
                  <th>Requested</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(query.data?.items ?? []).map((transfer) => (
                  <tr key={transfer.id}>
                    <td className="font-mono text-[11px] text-content-300">{transfer.reference}</td>
                    <td className="font-mono text-[11px] text-content-400">{transfer.userId.slice(-8)}</td>
                    <td className="text-right font-semibold tnum text-content-100">{formatMoney(transfer.amount)}</td>
                    <td className="text-[11px] text-content-300">
                      {isDeposit ? (
                        transfer.method ?? '--'
                      ) : (
                        <Destination transfer={transfer} />
                      )}
                    </td>
                    <td className="font-mono text-[11px] text-content-200">{transfer.utr ?? '--'}</td>
                    <td>
                      {transfer.proof ? (
                        <button
                          className="btn-ghost px-2 py-1 text-[11px]"
                          onClick={() => setPreview(transfer)}
                        >
                          {transfer.proof.mimeType.startsWith('image/') ? (
                            <ImageIcon className="h-3.5 w-3.5" />
                          ) : (
                            <FileText className="h-3.5 w-3.5" />
                          )}
                          View
                        </button>
                      ) : (
                        <span className="text-[11px] text-content-400">--</span>
                      )}
                    </td>
                    <td>
                      <StatusBadge status={transfer.status} />
                      {transfer.reviewNote ? (
                        <div className="max-w-[180px] truncate text-[10px] text-content-400">{transfer.reviewNote}</div>
                      ) : null}
                    </td>
                    <td className="text-[11px] text-content-400">
                      {formatDateTime(transfer.createdAt)}
                      {transfer.customerNote ? (
                        <div className="max-w-[160px] truncate text-content-300">{transfer.customerNote}</div>
                      ) : null}
                    </td>
                    <td className="text-right">
                      {transfer.status === 'PENDING' && can(approvePermission) ? (
                        <div className="flex justify-end gap-1">
                          <button
                            className="btn-primary px-2 py-1 text-[11px]"
                            disabled={approveDeposit.isPending}
                            onClick={() =>
                              isDeposit ? approveDeposit.mutate(transfer.id) : setApproving(transfer)
                            }
                          >
                            Approve
                          </button>
                          <button
                            className="btn-ghost px-2 py-1 text-[11px]"
                            disabled={rejectMutation.isPending}
                            onClick={() => {
                              const note = window.prompt('Why is this being rejected? The customer will see this.');
                              if (note && note.trim().length >= 3) {
                                rejectMutation.mutate({ id: transfer.id, note: note.trim() });
                              }
                            }}
                          >
                            Reject
                          </button>
                        </div>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <Pagination page={query.data?.page ?? 1} totalPages={query.data?.totalPages ?? 1} onChange={setPage} />
      </Panel>

      {preview ? <ProofViewer transfer={preview} onClose={() => setPreview(null)} /> : null}
      {approving ? (
        <PayoutDialog
          transfer={approving}
          pending={approveWithdrawal.isPending}
          onCancel={() => setApproving(null)}
          onConfirm={(payoutUtr, note) =>
            approveWithdrawal.mutate({ id: approving.id, utr: payoutUtr, note })
          }
        />
      ) : null}
    </>
  );
}

function Destination({ transfer }: { transfer: Transfer }) {
  const destination = transfer.destination;
  if (!destination) return <span className="text-content-400">--</span>;

  if (destination.type === 'UPI') {
    return <span className="font-mono text-content-200">{destination.upiId}</span>;
  }
  return (
    <span className="text-content-200">
      <span className="block font-mono">{destination.accountNumber}</span>
      <span className="block text-[10px] text-content-400">
        {destination.ifsc} · {destination.accountName}
      </span>
    </span>
  );
}

/**
 * Renders a payment screenshot.
 *
 * The image is fetched with the operator's token and shown from an object URL:
 * the proof route is authenticated, so a plain <img src> would get a 401.
 */
function ProofViewer({ transfer, onClose }: { transfer: Transfer; onClose: () => void }) {
  const [objectUrl, setObjectUrl] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const proofQuery = useQuery({
    queryKey: ['proof', transfer.id],
    queryFn: async () => {
      const tokens = loadTokens();
      const response = await fetch('/api' + api.finance.proofUrl(transfer.id), {
        headers: tokens ? { authorization: 'Bearer ' + tokens.accessToken } : {},
      });
      if (!response.ok) throw new Error('Could not load the attachment');
      return response.blob();
    },
  });

  if (proofQuery.data && !objectUrl) setObjectUrl(URL.createObjectURL(proofQuery.data));
  if (proofQuery.error && !error) setError((proofQuery.error as Error).message);

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-scrim/75 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
      <div className="panel flex max-h-[90vh] w-full max-w-2xl flex-col">
        <div className="flex items-center gap-3 border-b border-surface-800 px-4 py-3">
          <div className="min-w-0">
            <div className="truncate text-sm font-semibold text-content-100">{transfer.reference}</div>
            <div className="text-[11px] text-content-400">
              {formatMoney(transfer.amount)}
              {transfer.utr ? ' · UTR ' + transfer.utr : ''}
            </div>
          </div>
          <button className="ml-auto rounded-lg p-1.5 text-content-400 hover:bg-surface-800" onClick={onClose} aria-label="Close">
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="min-h-[240px] flex-1 overflow-auto bg-surface-950 p-4">
          {error ? (
            <p className="text-center text-sm text-down">{error}</p>
          ) : !objectUrl ? (
            <p className="text-center text-sm text-content-400">Loading attachment…</p>
          ) : transfer.proof?.mimeType.startsWith('image/') ? (
            <img src={objectUrl} alt={'Payment proof for ' + transfer.reference} className="mx-auto max-w-full" />
          ) : (
            <object data={objectUrl} type="application/pdf" className="h-[60vh] w-full">
              <a href={objectUrl} download={transfer.proof?.originalName} className="text-accent underline">
                Download {transfer.proof?.originalName}
              </a>
            </object>
          )}
        </div>

        <div className="border-t border-surface-800 px-4 py-2 text-[11px] text-content-400">
          Check the amount, the date and the destination account against our statement before approving.
        </div>
      </div>
    </div>
  );
}

/** Approving a payout requires the reference of the payment already made. */
function PayoutDialog({
  transfer,
  pending,
  onCancel,
  onConfirm,
}: {
  transfer: Transfer;
  pending: boolean;
  onCancel: () => void;
  onConfirm: (utr: string, note?: string) => void;
}) {
  const [utr, setUtr] = useState('');
  const [note, setNote] = useState('');

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-scrim/75 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
      <form
        className="panel w-full max-w-md p-5"
        onSubmit={(event) => {
          event.preventDefault();
          if (utr.trim().length < 6) return;
          onConfirm(utr.trim(), note.trim() || undefined);
        }}
      >
        <h2 className="text-sm font-semibold text-content-100">Confirm payout</h2>
        <p className="mt-1 text-xs text-content-400">
          {formatMoney(transfer.amount)} to{' '}
          <span className="font-mono text-content-200">
            {transfer.destination?.type === 'UPI'
              ? transfer.destination.upiId
              : transfer.destination?.accountNumber}
          </span>
        </p>

        <div className="mt-4">
          <label className="label" htmlFor="payoutUtr">
            UTR of the payment you made
          </label>
          <input
            id="payoutUtr"
            className="field font-mono uppercase"
            placeholder="e.g. 412345678901"
            value={utr}
            onChange={(event) => setUtr(event.target.value)}
            autoFocus
          />
          <p className="mt-1 text-[11px] text-content-400">
            Recorded on the wallet ledger, so this payout is provable later.
          </p>
        </div>

        <div className="mt-3">
          <label className="label" htmlFor="payoutNote">
            Note (optional)
          </label>
          <input
            id="payoutNote"
            className="field"
            value={note}
            onChange={(event) => setNote(event.target.value)}
          />
        </div>

        <div className="mt-5 flex justify-end gap-2">
          <button type="button" className="btn-ghost" onClick={onCancel} disabled={pending}>
            Cancel
          </button>
          <button type="submit" className="btn-primary" disabled={pending || utr.trim().length < 6}>
            Mark as paid
          </button>
        </div>
      </form>
    </div>
  );
}
