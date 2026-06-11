import React from 'react';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import { Input, InlineField, SecretInput } from '@grafana/ui';
import { PromhashOptions, PromhashSecureOptions } from './types';

export function ConfigEditor(props: DataSourcePluginOptionsEditorProps<PromhashOptions, PromhashSecureOptions>) {
  const { options, onOptionsChange } = props;
  return (
    <>
      <InlineField label="promhash API URL">
        <Input
          value={options.jsonData.apiUrl ?? ''}
          onChange={(e) => onOptionsChange({ ...options, jsonData: { ...options.jsonData, apiUrl: e.currentTarget.value } })}
        />
      </InlineField>
      <InlineField label="API token" tooltip="Bearer token accepted by promhash-api (PROMHASH_API_TOKENS / -token-file)">
        <SecretInput
          isConfigured={Boolean(options.secureJsonFields?.apiToken)}
          value={options.secureJsonData?.apiToken ?? ''}
          onChange={(e) =>
            onOptionsChange({ ...options, secureJsonData: { ...options.secureJsonData, apiToken: e.currentTarget.value } })
          }
          onReset={() =>
            onOptionsChange({
              ...options,
              secureJsonFields: { ...options.secureJsonFields, apiToken: false },
              secureJsonData: { ...options.secureJsonData, apiToken: '' },
            })
          }
        />
      </InlineField>
    </>
  );
}
