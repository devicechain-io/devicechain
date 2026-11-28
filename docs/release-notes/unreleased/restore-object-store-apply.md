---
section: whats-new
---

## en

- **`dcctl restore` can restore the object store.** `dcctl restore <instance> --from <backup-set> --apply --only object-store` writes the update artifacts and branding images from a backup set into the installed instance's volume (`--blob-directory`, a writable mount of its root) or S3 bucket. It verifies the set before it reads the target and checks every object's SHA-256 again as it writes it. It refuses a target holding any object that differs from the backup or is not in it unless `--overwrite-target` is given, and it never deletes. Objects already identical are left alone, so running the same command again finishes a restore that stopped part way. The filesystem target must support hard links, and on S3 an object over 5 GiB is refused before anything is written. Stream restore is not implemented yet, and `--apply` without `--only object-store` is refused. `--plan` works as before. See [Restoring the object store](disaster-recovery.md#restore-object-store).

## es

- **`dcctl restore` puede restaurar el almacén de objetos.** `dcctl restore <instancia> --from <copia> --apply --only object-store` escribe los artefactos de actualización y las imágenes de marca de una copia de seguridad en el volumen de la instancia instalada (`--blob-directory`, un montaje con escritura de su raíz) o en su bucket S3. Verifica la copia antes de leer el destino y vuelve a comprobar el SHA-256 de cada objeto al escribirlo. Rechaza un destino que contenga algún objeto que difiera de la copia o que no esté en ella, salvo con `--overwrite-target`, y nunca elimina nada. Los objetos ya idénticos no se tocan, así que ejecutar de nuevo el mismo comando completa una restauración interrumpida. El sistema de archivos de destino debe admitir enlaces duros, y en S3 un objeto de más de 5 GiB se rechaza antes de escribir nada. La restauración de streams aún no está implementada y se rechaza `--apply` sin `--only object-store`. `--plan` funciona como antes. Consulte [Restaurar el almacén de objetos](disaster-recovery.md#restore-object-store).

## zh-CN

- **`dcctl restore` 现在可以恢复对象存储。** `dcctl restore <实例> --from <备份集> --apply --only object-store` 将备份集中的更新制品和品牌图片写入已安装实例的卷（`--blob-directory`，其根目录的可写挂载）或 S3 存储桶。它在读取目标之前验证备份集，并在写入每个对象时再次检查其 SHA-256。除非指定 `--overwrite-target`，否则目标中只要有对象与备份不同或不在备份中就会被拒绝，且绝不删除任何内容。已完全一致的对象不会被改动，因此再次运行同一命令即可完成中途停止的恢复。目标文件系统必须支持硬链接；在 S3 上，超过 5 GiB 的对象会在写入任何内容之前被拒绝。流恢复尚未实现，不带 `--only object-store` 的 `--apply` 会被拒绝。`--plan` 的行为不变。参见[恢复对象存储](disaster-recovery.md#restore-object-store)。
