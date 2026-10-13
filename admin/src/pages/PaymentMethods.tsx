import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatMoney, fromMinor, toMinor } from '../utils/format';
import { Badge, Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';
import type { PaymentMethod } from '../types/api';

/**
 * Deposit destinations.
 *
 * These are the account details a customer will actually send money to, so the
 * form validates them and every change is written to the audit log before the
 * response returns. Getting a digit wrong here sends real money elsewhere.
 */
export function PaymentMethodsPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const writable = can('settings:write');
  const [editing, setEditing] = useState<PaymentMethod | 'new' | null>(null);

  const query = useQuery({ queryKey: ['payment-methods'], queryFn: api.finance.paymentMethods });

  const saveMutation = useMutation({
    mutationFn: (input: Record<string, unknown>) => api.finance.savePaymentMethod(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['payment-methods'] });
      setEditing(null);
      toast('success', 'Payment method saved');
    },
    onError: (err) => toast('error', 'Save failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const toggleMutation = useMutation({
    mutationFn: ({ key, active }: { key: string; active: boolean }) =>
      api.finance.setPaymentMethodActive(key, active),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['payment-methods'] });
      toast('success', 'Payment method updated');
    },
    onError: (err) => toast('error', 'Update failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  return (
    <div className="space-y-4">
      <Panel className="overflow-hidden">
        <PanelHeader
          title="Deposit payment methods"
          description="Shown to customers on the deposit screen. Verify every detail - customers pay against exactly this."
          actions={
            writable ? (
              <button className="btn-primary" onClick={() => setEditing(editing === 'new' ? null : 'new')}>
                {editing === 'new' ? 'Cancel' : 'New method'}
              </button>
            ) : null
          }
        />

        {editing ? (
          <MethodForm
            initial={editing === 'new' ? null : editing}
            pending={saveMutation.isPending}
            onCancel={() => setEditing(null)}
            onSave={(input) => saveMutation.mutate(input)}
          />
        ) : null}

        {query.isLoading ? (
          <TableSkeleton rows={4} columns={6} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[900px]">
              <thead>
                <tr>
                  <th>Method</th>
                  <th>Type</th>
                  <th>Details</th>
                  <th className="text-right">Min</th>
                  <th className="text-right">Max</th>
                  <th>Status</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(query.data ?? []).map((method) => (
                  <tr key={method.id}>
                    <td>
                      <div className="font-medium text-content-100">{method.label}</div>
                      <div className="font-mono text-[10px] text-content-400">{method.key}</div>
                    </td>
                    <td>
                      <Badge tone="neutral">{method.type}</Badge>
                    </td>
                    <td className="font-mono text-[11px] text-content-200">
                      {method.type === 'UPI' ? (
                        method.upiId
                      ) : (
                        <>
                          <div>{method.accountNumber}</div>
                          <div className="text-[10px] text-content-400">
                            {method.ifsc} · {method.accountName}
                          </div>
                        </>
                      )}
                    </td>
                    <td className="text-right tnum text-content-300">{formatMoney(method.minAmount)}</td>
                    <td className="text-right tnum text-content-300">{formatMoney(method.maxAmount)}</td>
                    <td>
                      <StatusBadge status={method.active ? 'ACTIVE' : 'DISABLED'} />
                    </td>
                    <td className="text-right">
                      {writable ? (
                        <div className="flex justify-end gap-1">
                          <button className="btn-ghost px-2 py-1 text-[11px]" onClick={() => setEditing(method)}>
                            Edit
                          </button>
                          <button
                            className="btn-ghost px-2 py-1 text-[11px]"
                            onClick={() => toggleMutation.mutate({ key: method.key, active: !method.active })}
                          >
                            {method.active ? 'Disable' : 'Enable'}
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
      </Panel>
    </div>
  );
}

function MethodForm({
  initial,
  pending,
  onCancel,
  onSave,
}: {
  initial: PaymentMethod | null;
  pending: boolean;
  onCancel: () => void;
  onSave: (input: Record<string, unknown>) => void;
}) {
  const [type, setType] = useState<'UPI' | 'BANK'>(initial?.type ?? 'UPI');

  return (
    <form
      className="grid gap-3 border-b border-surface-800 bg-surface-850/60 p-4 sm:grid-cols-3"
      onSubmit={(event) => {
        event.preventDefault();
        const data = new FormData(event.currentTarget);
        const text = (name: string) => String(data.get(name) ?? '').trim();

        onSave({
          key: text('key'),
          label: text('label'),
          type,
          ...(type === 'UPI'
            ? { upiId: text('upiId') }
            : {
                accountName: text('accountName'),
                accountNumber: text('accountNumber'),
                ifsc: text('ifsc').toUpperCase(),
                bankName: text('bankName') || undefined,
              }),
          instructions: text('instructions') || undefined,
          minAmount: toMinor(Number(data.get('minAmount'))),
          maxAmount: toMinor(Number(data.get('maxAmount'))),
        });
      }}
    >
      <div>
        <label className="label" htmlFor="key">
          Key
        </label>
        <input
          id="key"
          name="key"
          required
          readOnly={!!initial}
          defaultValue={initial?.key ?? ''}
          placeholder="upi"
          className="field font-mono read-only:opacity-60"
        />
      </div>
      <div>
        <label className="label" htmlFor="label">
          Label shown to customers
        </label>
        <input id="label" name="label" required defaultValue={initial?.label ?? ''} className="field" />
      </div>
      <div>
        <label className="label" htmlFor="type">
          Type
        </label>
        <select
          id="type"
          className="field"
          value={type}
          onChange={(event) => setType(event.target.value as 'UPI' | 'BANK')}
        >
          <option value="UPI">UPI</option>
          <option value="BANK">Bank transfer</option>
        </select>
      </div>

      {type === 'UPI' ? (
        <div className="sm:col-span-3">
          <label className="label" htmlFor="upiId">
            UPI ID
          </label>
          <input
            id="upiId"
            name="upiId"
            required
            defaultValue={initial?.upiId ?? ''}
            placeholder="payments@bank"
            className="field font-mono"
          />
        </div>
      ) : (
        <>
          <div>
            <label className="label" htmlFor="accountName">
              Account name
            </label>
            <input id="accountName" name="accountName" required defaultValue={initial?.accountName ?? ''} className="field" />
          </div>
          <div>
            <label className="label" htmlFor="accountNumber">
              Account number
            </label>
            <input
              id="accountNumber"
              name="accountNumber"
              required
              defaultValue={initial?.accountNumber ?? ''}
              className="field font-mono"
            />
          </div>
          <div>
            <label className="label" htmlFor="ifsc">
              IFSC
            </label>
            <input
              id="ifsc"
              name="ifsc"
              required
              maxLength={11}
              defaultValue={initial?.ifsc ?? ''}
              className="field font-mono uppercase"
            />
          </div>
          <div className="sm:col-span-3">
            <label className="label" htmlFor="bankName">
              Bank name
            </label>
            <input id="bankName" name="bankName" defaultValue={initial?.bankName ?? ''} className="field" />
          </div>
        </>
      )}

      <div>
        <label className="label" htmlFor="minAmount">
          Minimum deposit
        </label>
        <input
          id="minAmount"
          name="minAmount"
          type="number"
          step="1"
          defaultValue={fromMinor(initial?.minAmount ?? 100_00)}
          className="field tnum"
        />
      </div>
      <div>
        <label className="label" htmlFor="maxAmount">
          Maximum deposit
        </label>
        <input
          id="maxAmount"
          name="maxAmount"
          type="number"
          step="1"
          defaultValue={fromMinor(initial?.maxAmount ?? 500_000_00)}
          className="field tnum"
        />
      </div>
      <div className="flex items-end gap-2">
        <button type="submit" className="btn-primary flex-1" disabled={pending}>
          Save
        </button>
        <button type="button" className="btn-ghost" onClick={onCancel} disabled={pending}>
          Cancel
        </button>
      </div>

      <div className="sm:col-span-3">
        <label className="label" htmlFor="instructions">
          Instructions shown on the deposit screen
        </label>
        <input
          id="instructions"
          name="instructions"
          defaultValue={initial?.instructions ?? ''}
          placeholder="Pay to the UPI ID above, then submit the UTR from your payment app."
          className="field"
        />
      </div>
    </form>
  );
}
