import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { toast } from '../../hooks/useToast';
import { EmptyState, Panel, PanelHeader, TableSkeleton } from '../../components/ui';
import { ConfirmDialog } from '../../components/overlay';
import { QueryError, Switch, Usdt, errorText, useUsdtNetworks } from '../../components/usdt';
import { formatUsdt, parseUsdt, toUsdtInput } from '../../utils/usdt';
import type { UsdtNetworkPatch, UsdtNetworkView } from '../../types/usdt';

/**
 * USDT networks.
 *
 * Switching a network off stops new deposits or withdrawals on it immediately;
 * anything already in flight still completes. Editing needs settings:write -
 * everyone else sees the same table read-only.
 */
type PendingToggle = { network: UsdtNetworkView; field: 'depositEnabled' | 'withdrawEnabled' };

export function UsdtNetworksPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const writable = can('settings:write');
  const query = useUsdtNetworks();
  const [disabling, setDisabling] = useState<PendingToggle | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);

  const update = useMutation({
    mutationFn: ({ code, patch }: { code: string; patch: UsdtNetworkPatch }) => api.usdt.updateNetwork(code, patch),
    onSuccess: (_result, { patch }) => {
      void queryClient.invalidateQueries({ queryKey: ['usdt', 'networks'] });
      setDisabling(null);
      setDialogError(null);
      toast('success', 'Network updated', describePatch(patch));
    },
    onError: (err) => {
      const message = errorText(err, 'The network was not updated. Try again.');
      setDialogError(message);
      toast('error', 'Update failed', message);
    },
  });

  const busyKey = update.isPending ? update.variables?.code + ':' + Object.keys(update.variables?.patch ?? {}).join(',') : null;

  const toggle = (network: UsdtNetworkView, field: PendingToggle['field'], next: boolean) => {
    if (!next) {
      // Turning a network off affects customers immediately - say so first.
      setDialogError(null);
      setDisabling({ network, field });
      return;
    }
    update.mutate({ code: network.code, patch: { [field]: true } });
  };

  return (
    <>
      <Panel className="overflow-hidden">
        <PanelHeader
          title="USDT networks"
          description={
            writable
              ? 'Turn deposits and withdrawals on or off per network and set the minimum deposit.'
              : 'Read-only: changing networks needs the settings:write permission.'
          }
        />

        {query.isLoading ? (
          <TableSkeleton rows={2} columns={6} />
        ) : query.isError && !query.data ? (
          <QueryError error={query.error} onRetry={() => void query.refetch()} what="networks" />
        ) : (query.data ?? []).length === 0 ? (
          <EmptyState title="No networks configured" />
        ) : (
          <div className="relative overflow-x-auto">
            <table className="table min-w-[980px]">
              <thead>
                <tr>
                  <th>Network</th>
                  <th>Chain</th>
                  <th className="text-right">Decimals</th>
                  <th>Deposits</th>
                  <th>Withdrawals</th>
                  <th>Minimum deposit</th>
                  <th>Address example</th>
                </tr>
              </thead>
              <tbody>
                {(query.data ?? []).map((network) => (
                  <tr key={network.code}>
                    <td>
                      <div className="font-semibold text-content-100">{network.label}</div>
                      <div className="text-[11px] text-content-400">
                        {network.name} · <span className="font-mono">{network.code}</span>
                      </div>
                    </td>
                    <td className="text-content-200">{network.chain}</td>
                    <td className="text-right tnum text-content-300">{network.decimals}</td>
                    <td>
                      <Switch
                        label={network.label + ' deposits'}
                        checked={network.depositEnabled}
                        disabled={!writable}
                        busy={busyKey === network.code + ':depositEnabled'}
                        onChange={(next) => toggle(network, 'depositEnabled', next)}
                      />
                    </td>
                    <td>
                      <Switch
                        label={network.label + ' withdrawals'}
                        checked={network.withdrawEnabled}
                        disabled={!writable}
                        busy={busyKey === network.code + ':withdrawEnabled'}
                        onChange={(next) => toggle(network, 'withdrawEnabled', next)}
                      />
                    </td>
                    <td>
                      {writable ? (
                        <MinDepositEditor
                          network={network}
                          saving={busyKey === network.code + ':minDeposit'}
                          onSave={(minDeposit) => update.mutate({ code: network.code, patch: { minDeposit } })}
                        />
                      ) : (
                        <Usdt micro={network.minDeposit} className="text-content-100" />
                      )}
                    </td>
                    <td className="max-w-[220px] truncate font-mono text-[11px] text-content-400" title={network.addressExample}>
                      {network.addressExample || '--'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      {disabling ? (
        <ConfirmDialog
          title={'Turn off ' + disabling.network.label + (disabling.field === 'depositEnabled' ? ' deposits' : ' withdrawals')}
          tone="danger"
          confirmLabel="Turn off"
          pending={update.isPending}
          error={dialogError}
          onCancel={() => setDisabling(null)}
          onConfirm={() => update.mutate({ code: disabling.network.code, patch: { [disabling.field]: false } })}
        >
          <p>
            Customers will not be able to start new{' '}
            {disabling.field === 'depositEnabled' ? 'deposits' : 'withdrawals'} on {disabling.network.label} (
            {disabling.network.name}) until it is turned back on.
          </p>
          <p>Requests already in progress are not affected.</p>
        </ConfirmDialog>
      ) : null}
    </>
  );
}

function MinDepositEditor({
  network,
  saving,
  onSave,
}: {
  network: UsdtNetworkView;
  saving: boolean;
  onSave: (micro: number) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState('');
  const parsed = parseUsdt(text);
  const problem =
    text.trim() === ''
      ? 'Enter an amount'
      : parsed == null
        ? 'Up to 6 decimals, digits only'
        : parsed <= 0
          ? 'Must be above zero'
          : null;
  const inputId = 'min-' + network.code;

  if (!editing) {
    return (
      <div className="flex items-center gap-2">
        <Usdt micro={network.minDeposit} className="text-content-100" />
        <button
          className="btn-ghost px-2 py-0.5 text-[11px]"
          disabled={saving}
          onClick={() => {
            setText(toUsdtInput(network.minDeposit));
            setEditing(true);
          }}
          aria-label={'Edit ' + network.label + ' minimum deposit'}
        >
          {saving ? 'Saving…' : 'Edit'}
        </button>
      </div>
    );
  }

  return (
    <form
      className="flex flex-wrap items-start gap-1.5"
      onSubmit={(event) => {
        event.preventDefault();
        if (problem || parsed == null) return;
        if (parsed !== network.minDeposit) onSave(parsed);
        setEditing(false);
      }}
    >
      <div>
        <label className="sr-only" htmlFor={inputId}>
          {network.label} minimum deposit in USDT
        </label>
        <div className="relative">
          <input
            id={inputId}
            className="field w-36 py-1 pr-12 font-mono text-xs"
            inputMode="decimal"
            value={text}
            autoFocus
            aria-invalid={!!problem}
            onChange={(event) => setText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Escape') setEditing(false);
            }}
          />
          <span className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-content-400">USDT</span>
        </div>
        <div className={'mt-0.5 text-[10px] ' + (problem ? 'text-down' : 'text-content-400')}>
          {problem ?? '= ' + formatUsdt(parsed)}
        </div>
      </div>
      <button type="submit" className="btn-primary px-2 py-1 text-[11px]" disabled={!!problem}>
        Save
      </button>
      <button type="button" className="btn-ghost px-2 py-1 text-[11px]" onClick={() => setEditing(false)}>
        Cancel
      </button>
    </form>
  );
}

function describePatch(patch: UsdtNetworkPatch): string {
  if (patch.depositEnabled != null) return 'Deposits ' + (patch.depositEnabled ? 'enabled' : 'disabled');
  if (patch.withdrawEnabled != null) return 'Withdrawals ' + (patch.withdrawEnabled ? 'enabled' : 'disabled');
  if (patch.minDeposit != null) return 'Minimum deposit set to ' + formatUsdt(patch.minDeposit);
  return '';
}
