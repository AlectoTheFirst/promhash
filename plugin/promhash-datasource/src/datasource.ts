import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';
import { DataSourceInstanceSettings, MetricFindValue } from '@grafana/data';
import { PromhashQuery, PromhashOptions } from './types';

export class DataSource extends DataSourceWithBackend<PromhashQuery, PromhashOptions> {
  constructor(s: DataSourceInstanceSettings<PromhashOptions>) { super(s); }

  async apps(): Promise<string[]> { return this.getResource('apps'); }

  // Template-variable support. Two query forms:
  //   apps                  -> application names (e.g. an $app picker)
  //   path_interfaces/$app  -> composite "instance:ifIndex" selectors for the
  //                            selected app, for use as iface=~"$iface" in
  //                            Prometheus panels (zero-cardinality pattern)
  async metricFindQuery(query: string, options?: any): Promise<MetricFindValue[]> {
    const interpolated = getTemplateSrv().replace((query ?? '').trim(), options?.scopedVars);
    if (!interpolated) { return []; }
    const values = await this.getResource(interpolated);
    return (Array.isArray(values) ? values : []).map((v: string) => ({ text: v }));
  }
}
