import { useTranslation } from "react-i18next";
import { USBIcon, RAMIcon } from "icons";
import { FormControlLabel, Switch } from "@material-ui/core";
import TextField from "@material-ui/core/TextField";

import {
  CacheLegendGrid,
  CacheLegendDot,
  MainSettingsContent,
  StorageButton,
  StorageIconWrapper,
  CacheStorageSelector,
  SettingSectionLabel,
  PreloadCachePercentage,
  cacheBeforeReaderColor,
  cacheAfterReaderColor,
} from "./style";
import SliderInput from "./SliderInput";

const CacheStorageLocationLabel = ({ style }) => {
  const { t } = useTranslation();

  return (
    <SettingSectionLabel style={style}>
      {t("SettingsDialog.CacheStorageLocation")}
      <small>{t("SettingsDialog.UseDiskDesc")}</small>
    </SettingSectionLabel>
  );
};

export default function PrimarySettingsComponent({
  settings,
  inputForm,
  cachePercentage,
  preloadCachePercentage,
  preloadCacheMB,
  cacheSize,
  isProMode,
  readAheadMB,
  pausePreloadMB,
  pausePreloadAfterSec,
  setCacheSize,
  setCachePercentage,
  setReadAheadMB,
  setPausePreloadMB,
  setPausePreloadAfterSec,
  setPreloadCachePercentage,
  setPreloadCacheMB,
  updateSettings,
}) {
  const { t } = useTranslation();
  const { UseDisk, TorrentsSavePath, RemoveCacheOnDrop } = settings || {};
  const preloadCacheSize = Math.round(
    (cacheSize / 100) * preloadCachePercentage
  );

  return (
    <MainSettingsContent>
      <div>
        <SettingSectionLabel>
          {t("SettingsDialog.CacheSettings")}
        </SettingSectionLabel>

        <PreloadCachePercentage
          value={100 - cachePercentage}
          label={`${t("Cache")} ${cacheSize} ${t("MB")}`}
          preloadCachePercentage={preloadCachePercentage}
        />

        <CacheLegendGrid>
          <CacheLegendDot color={cacheBeforeReaderColor} aria-hidden />
          <div className="cache-legend-value">
            {readAheadMB} {t("MB")}
          </div>
          <div className="cache-legend-desc">
            {t("SettingsDialog.CacheBeforeReaderDesc")}
          </div>

          <CacheLegendDot color={cacheAfterReaderColor} aria-hidden />
          <div className="cache-legend-value">
            {pausePreloadMB} {t("MB")}
          </div>
          <div className="cache-legend-desc">
            {t("SettingsDialog.CacheAfterReaderDesc")}
          </div>
        </CacheLegendGrid>

        <br />

        <SliderInput
          isProMode={isProMode}
          title={t("SettingsDialog.CacheSize")}
          value={cacheSize}
          setValue={setCacheSize}
          sliderMin={32}
          sliderMax={1024}
          inputMin={32}
          inputMax={999999}
          step={4}
          onBlurCallback={(value) => setCacheSize(Math.round(value / 4) * 4)}
        />

        <SliderInput
          isProMode={isProMode}
          title={t("SettingsDialog.ReaderReadAHead")}
          value={readAheadMB}
          setValue={setReadAheadMB}
          sliderMin={4}
          sliderMax={4096}
          inputMin={4}
          inputMax={4096}
          step={4}
        />

        <SliderInput
          isProMode={isProMode}
          title={`${t("SettingsDialog.PausePreload")} - ${pausePreloadMB} ${t(
            "MB"
          )}`}
          value={pausePreloadMB}
          setValue={setPausePreloadMB}
          sliderMin={0}
          sliderMax={4096}
          inputMin={0}
          inputMax={4096}
          step={4}
        />

        <SliderInput
          isProMode={isProMode}
          title={`${t(
            "SettingsDialog.PausePreloadAfter"
          )} - ${pausePreloadAfterSec} ${t("Sec")}`}
          value={pausePreloadAfterSec}
          setValue={setPausePreloadAfterSec}
          sliderMin={0}
          sliderMax={120}
          inputMin={0}
          inputMax={3600}
          step={1}
        />

        <FormControlLabel
          control={
            <Switch
              checked={!!settings.AutoTune}
              onChange={inputForm}
              id="AutoTune"
              color="secondary"
            />
          }
          label={t("SettingsDialog.AutoTune")}
          labelPlacement="start"
        />
        <div>
          <small>{t("SettingsDialog.AutoTuneDesc")}</small>
        </div>

        <FormControlLabel
          control={
            <Switch
              checked={!!settings.PausePreloadUnbounded}
              onChange={inputForm}
              id="PausePreloadUnbounded"
              color="secondary"
            />
          }
          label={t("SettingsDialog.PausePreloadUnbounded")}
          labelPlacement="start"
        />
        <div>
          <small>{t("SettingsDialog.PausePreloadUnboundedDesc")}</small>
        </div>

        <SliderInput
          isProMode={isProMode}
          title={`${t(
            "SettingsDialog.PreloadCache"
          )} - ${preloadCachePercentage.toFixed(2)}% (${preloadCacheSize} ${t(
            "MB"
          )})`}
          value={preloadCachePercentage}
          setValue={setPreloadCachePercentage}
          sliderMin={0}
          sliderMax={100}
          inputMin={0}
          inputMax={100}
          step={0.01}
          onBlurCallback={(value) =>
            setPreloadCachePercentage(Math.round(value * 100) / 100)
          }
        />
      </div>

      {UseDisk ? (
        <div>
          <CacheStorageLocationLabel />

          <div style={{ display: "grid", gridAutoFlow: "column" }}>
            <StorageButton
              small
              onClick={() => updateSettings({ UseDisk: false })}
            >
              <StorageIconWrapper small>
                <RAMIcon color="#323637" />
              </StorageIconWrapper>

              <div>{t("SettingsDialog.RAM")}</div>
            </StorageButton>

            <StorageButton small selected>
              <StorageIconWrapper small selected>
                <USBIcon color="#dee3e5" />
              </StorageIconWrapper>

              <div>{t("SettingsDialog.Disk")}</div>
            </StorageButton>
          </div>

          <FormControlLabel
            control={
              <Switch
                checked={RemoveCacheOnDrop}
                onChange={inputForm}
                id="RemoveCacheOnDrop"
                color="secondary"
              />
            }
            label={t("SettingsDialog.RemoveCacheOnDrop")}
            labelPlacement="start"
          />
          <div>
            <small>{t("SettingsDialog.RemoveCacheOnDropDesc")}</small>
          </div>
          <br />
          <TextField
            onChange={inputForm}
            margin="normal"
            id="TorrentsSavePath"
            label={t("SettingsDialog.TorrentsSavePath")}
            value={TorrentsSavePath}
            type="url"
            variant="outlined"
            fullWidth
          />
        </div>
      ) : (
        <CacheStorageSelector>
          <CacheStorageLocationLabel
            style={{ placeSelf: "start", gridArea: "label" }}
          />

          <StorageButton selected>
            <StorageIconWrapper selected>
              <RAMIcon color="#dee3e5" />
            </StorageIconWrapper>

            <div>{t("SettingsDialog.RAM")}</div>
          </StorageButton>

          <StorageButton onClick={() => updateSettings({ UseDisk: true })}>
            <StorageIconWrapper>
              <USBIcon color="#323637" />
            </StorageIconWrapper>

            <div>{t("SettingsDialog.Disk")}</div>
          </StorageButton>
        </CacheStorageSelector>
      )}
    </MainSettingsContent>
  );
}
